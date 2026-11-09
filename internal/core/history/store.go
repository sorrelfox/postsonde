package history

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DirName is the store's directory in a workspace's local directory.
const DirName = "history"

// Default limits (Limits).
const (
	DefaultMaxEntries   = 1000
	DefaultMaxBytes     = 64 << 20
	DefaultSegmentBytes = 4 << 20
)

// MaxEntryBytes is the largest line an entry is written as (1 MiB): a
// larger entry loses its bodies, then its headers (Entry.Trimmed).
const MaxEntryBytes = 1 << 20

// Error codes.
const (
	// CodeNotFound: no live entry has the ID (kind not_found). Details: id.
	CodeNotFound = "history.not_found"
	// CodeWriteFailed: a history file could not be written (kind io).
	CodeWriteFailed = "history.write_failed"
	// CodeReadFailed: a history file could not be read (kind io).
	CodeReadFailed = "history.read_failed"
)

// FormatVersion is the version every line is written with. Lines of a
// newer version are skipped.
const FormatVersion = 1

// Limits bound a store. A zero field takes its default.
type Limits struct {
	// MaxEntries is how many entries are kept; the oldest go first.
	MaxEntries int
	// MaxBytes bounds the size of the kept entries' lines.
	MaxBytes int64
	// SegmentBytes is the size at which a new segment file is started.
	SegmentBytes int64
}

func (l Limits) withDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.SegmentBytes <= 0 {
		l.SegmentBytes = DefaultSegmentBytes
	}
	return l
}

// record is one line of a segment: an entry, or the deletion of one.
type record struct {
	V       int    `json:"v"`
	Entry   *Entry `json:"entry,omitempty"`
	Deleted string `json:"deleted,omitempty"`
}

// segment is one segment file.
type segment struct {
	seq  int
	path string
	size int64
	// live counts its entries still in the index.
	live int
}

// item is a live entry of the index.
type item struct {
	sum Summary
	seg *segment
	off int64
	n   int64
}

// Store is the history of one workspace. It is safe for concurrent use.
// Open one Store per directory (Registry does).
type Store struct {
	dir string
	lim Limits

	mu    sync.Mutex
	segs  []*segment // oldest first
	live  []*item    // oldest first
	byID  map[string]*item
	bytes int64
	// cur is the last segment, open for appending; nil until the first
	// write after opening, a rotation or Clear.
	cur     *os.File
	nextSeq int
	// skipped counts lines that could not be read at Open.
	skipped int
}

// segmentName matches segment file names: 8 digits and ".jsonl".
func segmentSeq(name string) (int, bool) {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok || len(base) != 8 {
		return 0, false
	}
	n, err := strconv.Atoi(base)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func segmentName(seq int) string { return fmt.Sprintf("%08d.jsonl", seq) }

// Open reads the store in dir, which need not exist yet (it is created
// on the first write, 0700, with files 0600). A line that cannot be read
// is skipped; a last line cut short by a crash is also cut from the file,
// so the next entry starts on a line of its own. Retention is applied at
// once.
func Open(dir string, lim Limits) (*Store, error) {
	s := &Store{dir: dir, lim: lim.withDefaults(), byID: map[string]*item{}, nextSeq: 1}
	names, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, readErr(err, dir)
	}
	var seqs []int
	for _, d := range names {
		if seq, ok := segmentSeq(d.Name()); ok && d.Type().IsRegular() {
			seqs = append(seqs, seq)
		}
	}
	slices.Sort(seqs)
	for i, seq := range seqs {
		if err := s.load(seq, i == len(seqs)-1); err != nil {
			return nil, err
		}
		s.nextSeq = seq + 1
	}
	s.applyRetentionLocked()
	s.compactLocked()
	return s, nil
}

// load reads segment seq into the index. The last segment's torn tail is
// cut from the file.
func (s *Store) load(seq int, last bool) error {
	path := filepath.Join(s.dir, segmentName(seq))
	data, err := os.ReadFile(path)
	if err != nil {
		return readErr(err, path)
	}
	seg := &segment{seq: seq, path: path, size: int64(len(data))}
	end := bytes.LastIndexByte(data, '\n') + 1
	if end < len(data) {
		// A write cut short: never a whole entry.
		s.skipped++
		if last {
			if err := os.Truncate(path, int64(end)); err != nil {
				return writeErr(err, path)
			}
			seg.size = int64(end)
		}
	}
	s.segs = append(s.segs, seg)
	off := 0
	for off < end {
		n := bytes.IndexByte(data[off:end], '\n') + 1
		line := data[off : off+n]
		s.loadLine(seg, int64(off), line)
		off += n
	}
	return nil
}

// loadLine applies one line of seg at off.
func (s *Store) loadLine(seg *segment, off int64, line []byte) {
	var r record
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if err := json.Unmarshal(line, &r); err != nil || r.V < 1 {
		s.skipped++
		return
	}
	if r.V > FormatVersion {
		s.skipped++
		return
	}
	switch {
	case r.Deleted != "":
		s.removeLocked(r.Deleted)
	case r.Entry != nil && r.Entry.ID != "":
		if _, dup := s.byID[r.Entry.ID]; dup {
			s.skipped++
			return
		}
		s.addLocked(&item{sum: r.Entry.Summary, seg: seg, off: off, n: int64(len(line))})
	default:
		s.skipped++
	}
}

// Skipped counts the lines Open could not read (cut short, not JSON, of
// a newer format).
func (s *Store) Skipped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// Close closes the open segment file. The store can still be used: the
// next write opens it again.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCurLocked()
}

func (s *Store) closeCurLocked() error {
	if s.cur == nil {
		return nil
	}
	err := s.cur.Close()
	s.cur = nil
	return err
}

// Append writes e as a new entry and returns its summary, with the ID and
// time set when e had none. The entry must already be masked. Retention
// then drops the oldest entries over the limits.
func (s *Store) Append(e Entry) (Summary, error) {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	line, err := encodeEntry(&e)
	if err != nil {
		return Summary{}, errs.Wrap(err, errs.Internal, "The history entry could not be encoded.").WithCode(CodeWriteFailed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[e.ID]; dup {
		return Summary{}, errs.New(errs.AlreadyExists, "A history entry with this ID exists.").WithCode(CodeWriteFailed).WithDetail("id", e.ID)
	}
	seg, off, err := s.writeLocked(line)
	if err != nil {
		return Summary{}, err
	}
	s.addLocked(&item{sum: e.Summary, seg: seg, off: off, n: int64(len(line))})
	s.applyRetentionLocked()
	s.compactLocked()
	return e.Summary, nil
}

// encodeEntry returns e's line, trimming it to MaxEntryBytes.
func encodeEntry(e *Entry) ([]byte, error) {
	line, err := marshalLine(record{V: FormatVersion, Entry: e})
	if err != nil || len(line) <= MaxEntryBytes {
		return line, err
	}
	e.Trimmed = true
	e.Request.Body = Body{Size: e.Request.Body.Size, Clipped: e.Request.Body.Size > 0}
	if t := e.Template; t != nil {
		// The editor's texts go with the bodies; a restore says so.
		if t.Body != nil && t.Body.Text != "" {
			t.Body.Text = ""
			t.Clipped = append(t.Clipped, SectionBody)
		}
		if t.Query != "" || t.Variables != "" {
			t.Query, t.Variables = "", ""
			t.Clipped = append(t.Clipped, SectionQuery, SectionVariables)
		}
	}
	if e.Response != nil {
		e.Response.Body = Body{Size: e.Response.Body.Size, Clipped: e.Response.Body.Size > 0}
	}
	if line, err = marshalLine(record{V: FormatVersion, Entry: e}); err != nil || len(line) <= MaxEntryBytes {
		return line, err
	}
	e.Request.Headers = []Header{}
	if e.Response != nil {
		e.Response.Headers = []Header{}
	}
	return marshalLine(record{V: FormatVersion, Entry: e})
}

// marshalLine encodes r as one line ended by "\n". HTML characters are
// not escaped, so URLs stay readable.
func marshalLine(r record) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeLocked appends line to the current segment, rotating first when it
// is full, and returns the segment and the line's offset. A failed write
// is cut from the file again.
func (s *Store) writeLocked(line []byte) (*segment, int64, error) {
	var seg *segment
	if n := len(s.segs); n > 0 {
		seg = s.segs[n-1]
	}
	if seg == nil || (seg.size > 0 && seg.size+int64(len(line)) > s.lim.SegmentBytes) {
		if err := s.closeCurLocked(); err != nil {
			return nil, 0, writeErr(err, s.dir)
		}
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, 0, writeErr(err, s.dir)
		}
		seg = &segment{seq: s.nextSeq, path: filepath.Join(s.dir, segmentName(s.nextSeq))}
		f, err := os.OpenFile(seg.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
		if err != nil {
			return nil, 0, writeErr(err, seg.path)
		}
		s.nextSeq++
		s.cur = f
		s.segs = append(s.segs, seg)
	}
	if s.cur == nil {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, 0, writeErr(err, s.dir)
		}
		f, err := os.OpenFile(seg.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, 0, writeErr(err, seg.path)
		}
		s.cur = f
	}
	off := seg.size
	if _, err := s.cur.Write(line); err != nil {
		// Leave no partial line behind for the next entry to join.
		_ = s.cur.Truncate(off)
		_ = s.closeCurLocked()
		return nil, 0, writeErr(err, seg.path)
	}
	seg.size += int64(len(line))
	return seg, off, nil
}

// addLocked adds it to the index as the newest entry.
func (s *Store) addLocked(it *item) {
	s.live = append(s.live, it)
	s.byID[it.sum.ID] = it
	s.bytes += it.n
	it.seg.live++
}

// removeLocked drops entry id from the index; false when it is not there.
func (s *Store) removeLocked(id string) bool {
	it, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	if i := slices.Index(s.live, it); i >= 0 {
		s.live = slices.Delete(s.live, i, i+1)
	}
	s.bytes -= it.n
	it.seg.live--
	return true
}

// applyRetentionLocked drops the oldest entries while there are more than
// MaxEntries or their lines are over MaxBytes. The newest entry is always
// kept.
func (s *Store) applyRetentionLocked() {
	drop := 0
	bytes := s.bytes
	for len(s.live)-drop > 1 && (len(s.live)-drop > s.lim.MaxEntries || bytes > s.lim.MaxBytes) {
		it := s.live[drop]
		delete(s.byID, it.sum.ID)
		bytes -= it.n
		it.seg.live--
		drop++
	}
	if drop > 0 {
		s.live = slices.Delete(s.live, 0, drop)
		s.bytes = bytes
	}
}

// compactLocked removes the oldest segment files while none of their
// entries is live, never the last one. Only the oldest go, so a deletion
// line is never removed before the segment of the entry it deletes.
func (s *Store) compactLocked() {
	for len(s.segs) > 1 && s.segs[0].live == 0 {
		if err := os.Remove(s.segs[0].path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
		s.segs = s.segs[1:]
	}
}

// Get returns entry id, read from its file.
func (s *Store) Get(id string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.byID[id]
	if !ok {
		return Entry{}, notFound(id)
	}
	f, err := os.Open(it.seg.path)
	if err != nil {
		return Entry{}, readErr(err, it.seg.path)
	}
	defer f.Close()
	buf := make([]byte, it.n)
	if _, err := f.ReadAt(buf, it.off); err != nil {
		return Entry{}, readErr(err, it.seg.path)
	}
	var r record
	if err := json.Unmarshal(buf, &r); err != nil || r.Entry == nil || r.Entry.ID != id {
		if err == nil {
			err = errors.New("the entry's line changed on disk")
		}
		return Entry{}, readErr(err, it.seg.path)
	}
	return *r.Entry, nil
}

// Delete removes entry id: a deletion line is appended, and the entry's
// file goes once none of its entries is left.
func (s *Store) Delete(id string) error {
	line, err := marshalLine(record{V: FormatVersion, Deleted: id})
	if err != nil {
		return errs.Wrap(err, errs.Internal, "The history could not be changed.").WithCode(CodeWriteFailed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return notFound(id)
	}
	if _, _, err := s.writeLocked(line); err != nil {
		return err
	}
	s.removeLocked(id)
	s.compactLocked()
	return nil
}

// Clear removes every entry and segment file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.closeCurLocked()
	var first error
	kept := s.segs[:0]
	for _, seg := range s.segs {
		if err := os.Remove(seg.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if first == nil {
				first = err
			}
			kept = append(kept, seg)
		}
	}
	s.segs = kept
	for _, it := range s.live {
		it.seg.live--
	}
	s.live, s.byID, s.bytes = nil, map[string]*item{}, 0
	if first != nil {
		return writeErr(first, s.dir)
	}
	return nil
}

// Status filters of Query.
const (
	// StatusError: sends without a response (failed or cancelled).
	StatusError = "error"
)

// Query selects and pages entries for List.
type Query struct {
	// Text matches entries whose method, URL, request name, status, error
	// or environment contain it, ignoring case. With Deep it also matches
	// the header fields ("Name: value") and the text bodies of the request
	// and the response, and the fields of the entry's Template.
	Text string
	Deep bool
	// Mask, when set, is applied to every text before Text is looked for
	// in it, so a secret known now is not found in an entry recorded
	// before it was one. Entries are masked when they are recorded; Text
	// is only ever looked for in what the store keeps.
	Mask func(string) string
	// Method matches the method, ignoring case.
	Method string
	// Status is "1xx" to "5xx" for a status class, StatusError for sends
	// without a response, "" for all.
	Status string
	// Kind is KindHTTP, KindGraphQL or KindReplay; "" for all.
	Kind string
	// Environment matches the environment's name exactly; "" for all.
	Environment string
	// Since and Until bound the send's start time: Since included, Until
	// not. Zero for no bound.
	Since, Until time.Time
	// RequestID matches the entries of one collection request.
	RequestID string
	// Offset skips the first matching entries; Limit caps how many are
	// returned (DefaultPageSize when 0, at most MaxPageSize).
	Offset int
	Limit  int
}

// Page sizes of List.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// Page is a page of List.
type Page struct {
	// Entries are the matching entries, newest first, after Offset.
	Entries []Summary
	// Total counts the matching entries; All counts every live entry.
	Total int
	All   int
	// Environments are the names of the environments of every live
	// entry, sorted, for the filter; "" is left out.
	Environments []string
}

// List returns the entries q selects, newest first. With q.Deep and a
// text, the entries whose summary does not match are read from their
// files, one segment file at a time.
func (s *Store) List(q Query) Page {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)
	text := strings.ToLower(strings.TrimSpace(q.Text))
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Page{Entries: []Summary{}, All: len(s.live), Environments: []string{}}
	envs := map[string]bool{}
	var file segmentCache
	for i := len(s.live) - 1; i >= 0; i-- {
		it := s.live[i]
		sum := &it.sum
		if sum.Environment != "" && !envs[sum.Environment] {
			envs[sum.Environment] = true
			p.Environments = append(p.Environments, sum.Environment)
		}
		if !q.selects(sum) {
			continue
		}
		if text != "" && !q.matchesSummary(sum, text) && (!q.Deep || !q.matchesEntry(file.entry(it), text)) {
			continue
		}
		if p.Total >= q.Offset && len(p.Entries) < limit {
			p.Entries = append(p.Entries, *sum)
		}
		p.Total++
	}
	slices.Sort(p.Environments)
	return p
}

// selects reports whether sum passes the filters of q, the text aside.
func (q Query) selects(sum *Summary) bool {
	if q.RequestID != "" && sum.RequestID != q.RequestID {
		return false
	}
	if q.Method != "" && !strings.EqualFold(q.Method, sum.Method) {
		return false
	}
	if q.Kind != "" && q.Kind != sum.Kind {
		return false
	}
	if q.Environment != "" && q.Environment != sum.Environment {
		return false
	}
	if !q.Since.IsZero() && sum.Time.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !sum.Time.Before(q.Until) {
		return false
	}
	switch q.Status {
	case "":
	case StatusError:
		if sum.Outcome == OutcomeOK {
			return false
		}
	default:
		if sum.Outcome != OutcomeOK || len(q.Status) != 3 || q.Status[0] != byte('0'+sum.StatusCode/100) {
			return false
		}
	}
	return true
}

// contains reports whether s, masked with q.Mask, contains text (lower
// case), ignoring case.
func (q Query) contains(s, text string) bool {
	if s == "" {
		return false
	}
	if q.Mask != nil {
		s = q.Mask(s)
	}
	return strings.Contains(strings.ToLower(s), text)
}

// matchesSummary reports whether text is in sum's method, URL, request
// name, status, error or environment.
func (q Query) matchesSummary(sum *Summary, text string) bool {
	status := ""
	if sum.StatusCode != 0 {
		status = strconv.Itoa(sum.StatusCode)
	}
	for _, f := range []string{sum.Method, sum.URL, sum.RequestName, status, sum.Status, sum.Error, sum.Environment} {
		if q.contains(f, text) {
			return true
		}
	}
	return false
}

// matchesEntry reports whether text is in e's header fields, text bodies,
// final URL or template; false for nil.
func (q Query) matchesEntry(e *Entry, text string) bool {
	if e == nil {
		return false
	}
	headers := func(hs []Header) bool {
		for _, h := range hs {
			if q.contains(h.Name+": "+h.Value, text) {
				return true
			}
		}
		return false
	}
	body := func(b Body) bool { return !b.Base64 && q.contains(b.Text, text) }
	if headers(e.Request.Headers) || body(e.Request.Body) {
		return true
	}
	if r := e.Response; r != nil && (headers(r.Headers) || body(r.Body) || q.contains(r.FinalURL, text)) {
		return true
	}
	t := e.Template
	if t == nil {
		return false
	}
	fields := func(fs []Field) bool {
		for _, f := range fs {
			if q.contains(f.Name+": "+f.Value, text) || q.contains(f.Name+"="+f.Value, text) {
				return true
			}
		}
		return false
	}
	if q.contains(t.URL, text) || fields(t.Params) || fields(t.PathParams) || fields(t.Headers) ||
		q.contains(t.Query, text) || q.contains(t.Variables, text) || q.contains(t.OperationName, text) {
		return true
	}
	b := t.Body
	return b != nil && (q.contains(b.Text, text) || fields(b.Fields) || fields(b.Parts) || q.contains(b.Path, text))
}

// segmentCache holds the last segment file a deep List read, so the
// entries of a segment take one file read: List visits them newest
// first, one segment after the other.
type segmentCache struct {
	seg  *segment
	data []byte
}

// entry returns it as read from its segment, or nil when it cannot be
// read.
func (c *segmentCache) entry(it *item) *Entry {
	if c.seg != it.seg {
		data, err := os.ReadFile(it.seg.path)
		if err != nil {
			data = nil
		}
		c.seg, c.data = it.seg, data
	}
	if it.off < 0 || it.off+it.n > int64(len(c.data)) {
		return nil
	}
	var r record
	if err := json.Unmarshal(c.data[it.off:it.off+it.n], &r); err != nil || r.Entry == nil || r.Entry.ID != it.sum.ID {
		return nil
	}
	return r.Entry
}

// DeleteRequest removes every entry of collection request requestID and
// returns their IDs (none when it has none). The deletion lines are
// appended in one write.
func (s *Store) DeleteRequest(requestID string) ([]string, error) {
	if requestID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	var buf bytes.Buffer
	for _, it := range s.live {
		if it.sum.RequestID != requestID {
			continue
		}
		line, err := marshalLine(record{V: FormatVersion, Deleted: it.sum.ID})
		if err != nil {
			return nil, errs.Wrap(err, errs.Internal, "The history could not be changed.").WithCode(CodeWriteFailed)
		}
		buf.Write(line)
		ids = append(ids, it.sum.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, _, err := s.writeLocked(buf.Bytes()); err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.removeLocked(id)
	}
	s.compactLocked()
	return ids, nil
}

// Len counts the live entries.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.live)
}

// NewID returns a new entry ID: "h_" and 16 random hex digits.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "h_" + hex.EncodeToString(b[:])
}

func notFound(id string) error {
	return errs.New(errs.NotFound, "The history entry no longer exists.").WithCode(CodeNotFound).WithDetail("id", id)
}

func readErr(err error, path string) error {
	return errs.Wrap(err, errs.IO, "The request history could not be read.").WithCode(CodeReadFailed).WithDetail("path", path)
}

func writeErr(err error, path string) error {
	return errs.Wrap(err, errs.IO, "The request history could not be written.").WithCode(CodeWriteFailed).WithDetail("path", path)
}
