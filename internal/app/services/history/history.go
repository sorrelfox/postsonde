// Package history is the bound service over the request history (NAV-02):
// the log of every request sent from the open project, kept in its local
// directory by internal/core/history. It lists (with a filter and paging),
// reads, deletes and clears entries, and tells the UI about changes with
// history:changed. Sends are recorded by the HTTP service through
// Recorder. See docs/history.md.
package history

import (
	"context"
	"log/slog"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// LocalDir returns the open workspace's local side-file directory
// (workspace.Manager.LocalDir); it fails with workspace.none_open when no
// project is open.
type LocalDir func() (string, error)

// Emit sends one event to the UI.
type Emit func(name string, data any)

// HistoryService exposes the open project's request history to the UI.
type HistoryService struct {
	stores   *history.Registry
	localDir LocalDir
	emit     Emit
	logger   *slog.Logger
	// version and pickSave serve ExportHAR (WithHARExport); without a
	// picker it is unavailable.
	version  string
	pickSave SavePicker
	// secrets mask response examples (WithSecrets, example.go).
	secrets Secrets
}

// New returns a HistoryService over the stores of reg (a nil reg means
// the default limits), finding the open project's directory with
// localDir. A nil emit sends no events; a nil logger means slog.Default().
func New(reg *history.Registry, localDir LocalDir, emit Emit, logger *slog.Logger, opts ...Option) *HistoryService {
	if reg == nil {
		reg = history.NewRegistry(history.Limits{})
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &HistoryService{stores: reg, localDir: localDir, emit: emit, logger: logger.With("service", Domain)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *HistoryService) ServiceName() string { return Domain }

// ServiceShutdown closes the open history files.
func (s *HistoryService) ServiceShutdown() error { return s.stores.Close() }

// List returns the open project's entries that in selects, newest first,
// one page of them. The search runs here, over the store, on the entries
// as kept (masked when recorded) and masked again with the secrets known
// now (WithSecrets), so a secret is never found by its value; the URL and
// error of the entries returned are masked again the same way. An
// invalid since or until is history.invalid_filter.
func (s *HistoryService) List(ctx context.Context, in ListInputDTO) (PageDTO, error) {
	st, _, err := s.current(ctx)
	if err != nil {
		return PageDTO{}, err
	}
	since, err := parseBound("since", in.Since)
	if err != nil {
		return PageDTO{}, err
	}
	until, err := parseBound("until", in.Until)
	if err != nil {
		return PageDTO{}, err
	}
	m := s.masker(ctx)
	q := history.Query{
		Text: in.Text, Deep: in.Deep, Method: in.Method, Status: in.Status, Kind: in.Kind, Environment: in.Environment,
		Since: since, Until: until, RequestID: in.RequestID, Offset: max(in.Offset, 0), Limit: in.Limit,
	}
	if m != nil {
		q.Mask = m.MaskText
	}
	p := st.List(q)
	out := PageDTO{Entries: make([]SummaryDTO, 0, len(p.Entries)), Total: p.Total, All: p.All, Environments: p.Environments}
	for _, e := range p.Entries {
		if m != nil {
			e.URL, e.Error = m.MaskText(e.URL), m.MaskText(e.Error)
		}
		out.Entries = append(out.Entries, toSummaryDTO(e))
	}
	return out, nil
}

// parseBound reads a time bound of List; "" is none.
func parseBound(name, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errs.New(errs.InvalidArgument, "The date of the history filter is not valid.").
			WithCode(CodeInvalidFilter).WithDetail("field", name).WithDetail("value", value)
	}
	return t, nil
}

// masker returns the masker of the secrets PostSonde keeps now
// (WithSecrets), or nil without them. Secrets that could not be read are
// logged.
func (s *HistoryService) masker(ctx context.Context) *httpclient.SecretMasker {
	if s.secrets == nil {
		return nil
	}
	values, err := s.secrets(ctx)
	if err != nil {
		s.logger.Warn("some secrets could not be read to mask the history", "err", err)
	}
	return httpclient.NewSecretMasker(values)
}

// ClearRequest removes every entry of collection request requestID from
// the open project's history and emits history:changed ("cleared" with
// the request's ID) when there were any. It returns how many went.
func (s *HistoryService) ClearRequest(ctx context.Context, requestID string) (int, error) {
	if requestID == "" {
		return 0, errs.New(errs.InvalidArgument, "No request was given to clear the history of.").WithCode(CodeInvalidFilter).WithDetail("field", "requestId")
	}
	st, _, err := s.current(ctx)
	if err != nil {
		return 0, err
	}
	ids, err := st.DeleteRequest(requestID)
	if err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		s.send(ChangedEventDTO{Action: ActionCleared, RequestID: requestID})
	}
	return len(ids), nil
}

// Get returns entry id of the open project's history, with the request as
// sent and the response snapshot. An unknown ID is history.not_found.
func (s *HistoryService) Get(ctx context.Context, id string) (EntryDTO, error) {
	st, _, err := s.current(ctx)
	if err != nil {
		return EntryDTO{}, err
	}
	e, err := st.Get(id)
	if err != nil {
		return EntryDTO{}, err
	}
	return toEntryDTO(e), nil
}

// Delete removes entry id from the open project's history and emits
// history:changed.
func (s *HistoryService) Delete(ctx context.Context, id string) error {
	st, _, err := s.current(ctx)
	if err != nil {
		return err
	}
	if err := st.Delete(id); err != nil {
		return err
	}
	s.send(ChangedEventDTO{Action: ActionDeleted, ID: id})
	return nil
}

// Clear removes every entry of the open project's history, with its
// files, and emits history:changed.
func (s *HistoryService) Clear(ctx context.Context) error {
	st, _, err := s.current(ctx)
	if err != nil {
		return err
	}
	if err := st.Clear(); err != nil {
		return err
	}
	s.send(ChangedEventDTO{Action: ActionCleared})
	return nil
}

// current returns the open project's store and local directory.
func (s *HistoryService) current(ctx context.Context) (*history.Store, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", errs.Wrap(err, errs.KindOf(err), "Reading the history was interrupted.")
	}
	if s.localDir == nil {
		return nil, "", errs.New(errs.Unsupported, "The request history is not available.").WithCode("history.unavailable")
	}
	dir, err := s.localDir()
	if err != nil {
		return nil, "", err
	}
	st, err := s.stores.Store(dir)
	if err != nil {
		return nil, "", err
	}
	return st, dir, nil
}

func (s *HistoryService) send(ev ChangedEventDTO) {
	if s.emit != nil {
		s.emit(EventChanged, ev)
	}
}

// Recorder returns what the HTTP service records sends with
// (http.WithHistory): called when a send starts, it binds the open
// project's history and returns the function that appends the send's
// entry to it, or nil when no project is open. The entry must already be
// masked. A failed write is logged, never reported to the send. An entry
// added to the project that is open when it is written is announced with
// history:changed.
//
// It is a function, not a method, so it is not bound to the UI.
func Recorder(s *HistoryService) func() func(history.Entry) {
	return func() func(history.Entry) {
		if s == nil || s.localDir == nil {
			return nil
		}
		dir, err := s.localDir()
		if err != nil {
			return nil
		}
		return func(e history.Entry) {
			st, err := s.stores.Store(dir)
			if err != nil {
				s.logger.Warn("request history not written", "err", err)
				return
			}
			sum, err := st.Append(e)
			if err != nil {
				s.logger.Warn("request history not written", "err", err)
				return
			}
			if cur, err := s.localDir(); err == nil && cur == dir {
				dto := toSummaryDTO(sum)
				s.send(ChangedEventDTO{Action: ActionAdded, ID: sum.ID, Entry: &dto})
			}
		}
	}
}
