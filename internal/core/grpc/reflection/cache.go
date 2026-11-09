package reflection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Cache errors.
const (
	// CodeNotCached: no cache file for the address.
	CodeNotCached = "grpcreflection.not_cached"
	// CodeCacheCorrupt: the cache file is not a valid cache for the
	// address.
	CodeCacheCorrupt = "grpcreflection.cache_corrupt"
	// CodeCacheNewer: the cache file was written by a newer PostSonde.
	CodeCacheNewer = "grpcreflection.cache_newer"
	// CodeCacheIO: the cache could not be read or written.
	CodeCacheIO = "grpcreflection.cache_io"
)

// Cache file format.
const (
	cacheKind    = "postsonde.grpc-reflection-cache"
	cacheVersion = 1
)

// CacheDir is the cache's directory in a project's local directory,
// slash-separated.
const CacheDir = "grpc/reflection"

// cacheFile is the JSON form of a Snapshot.
type cacheFile struct {
	Kind       string         `json:"kind"`
	Version    int            `json:"version"`
	Address    string         `json:"address"`
	APIVersion Version        `json:"apiVersion"`
	FetchedAt  time.Time      `json:"fetchedAt"`
	Services   []string       `json:"services"`
	Files      []cacheFileRef `json:"files"`
	Problems   []cacheProblem `json:"problems"`
}

type cacheFileRef struct {
	Name string `json:"name"`
	// Descriptor is the serialized FileDescriptorProto, base64 in JSON.
	Descriptor []byte `json:"descriptor"`
}

type cacheProblem struct {
	Code    string `json:"code"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

// Cache keeps one snapshot per server address in the open project's local
// directory: <local>/grpc/reflection/<hash of the address>.json. Files are
// written atomically with mode 0600 in 0700 directories, like the other
// local side files.
type Cache struct {
	localDir func() (string, error)
}

// NewCache returns a cache under the directory localDir returns, which is
// asked on every call (workspace.Manager.LocalDir fits), so it follows the
// open project.
func NewCache(localDir func() (string, error)) *Cache {
	return &Cache{localDir: localDir}
}

// NormalizeAddress trims spaces and lower-cases the address, which is how
// snapshots and remembered versions are keyed.
func NormalizeAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

// Path returns the cache file of address.
func (c *Cache) Path(address string) (string, error) {
	dir, err := c.localDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(NormalizeAddress(address)))
	return filepath.Join(dir, filepath.FromSlash(CacheDir), hex.EncodeToString(sum[:8])+".json"), nil
}

// Load reads the snapshot cached for address. A missing file is not_found
// (CodeNotCached); a file that is not a valid cache for the address is a
// parse error (CodeCacheCorrupt); one from a newer PostSonde is unsupported
// (CodeCacheNewer). The file is never changed.
func (c *Cache) Load(address string) (*Snapshot, error) {
	path, err := c.Path(address)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errs.New(errs.NotFound, "No saved services for "+address+".").
			WithCode(CodeNotCached).
			WithHint("Load the services from the server by reflection first.")
	}
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Could not read the saved services for "+address+".").
			WithCode(CodeCacheIO).WithDetail("path", path)
	}
	return decodeCache(data, address, path)
}

func decodeCache(data []byte, address, path string) (*Snapshot, error) {
	corrupt := func(why string) error {
		return errs.New(errs.Parse, "The saved services for "+address+" cannot be used: "+why+".").
			WithCode(CodeCacheCorrupt).
			WithHint("Load the services from the server again to replace them.").
			WithDetail("path", path)
	}
	var head struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, corrupt("the file is not valid JSON")
	}
	if head.Kind != cacheKind {
		return nil, corrupt("the file is not a reflection cache")
	}
	if head.Version > cacheVersion {
		return nil, errs.New(errs.Unsupported, "The saved services for "+address+" were written by a newer version of PostSonde.").
			WithCode(CodeCacheNewer).
			WithHint("Update PostSonde to use them.").
			WithDetail("path", path).WithDetail("version", strconv.Itoa(head.Version))
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil || head.Version < 1 {
		return nil, corrupt("the file is not in the expected format")
	}
	if cf.Address != NormalizeAddress(address) {
		return nil, corrupt("the file belongs to another address")
	}
	if cf.APIVersion != V1 && cf.APIVersion != V1Alpha {
		return nil, corrupt("unknown API version " + strconv.Quote(string(cf.APIVersion)))
	}
	snap := &Snapshot{
		Address:   cf.Address,
		Version:   cf.APIVersion,
		FetchedAt: cf.FetchedAt,
		Services:  cf.Services,
	}
	for _, f := range cf.Files {
		if f.Name == "" || len(f.Descriptor) == 0 {
			return nil, corrupt("a file entry is empty")
		}
		snap.Files = append(snap.Files, File(f))
	}
	for _, p := range cf.Problems {
		snap.Problems = append(snap.Problems, Problem(p))
	}
	return snap, nil
}

// Save writes s as the cache of s.Address. A cache file from a newer
// PostSonde is not replaced (CodeCacheNewer). A corrupt one is moved aside
// to <file>.corrupt first, and its new path is returned so the caller can
// say so.
func (c *Cache) Save(s *Snapshot) (movedAside string, err error) {
	path, err := c.Path(s.Address)
	if err != nil {
		return "", err
	}
	if data, readErr := os.ReadFile(path); readErr == nil {
		if _, err := decodeCache(data, s.Address, path); err != nil {
			switch {
			case errors.Is(err, &errs.Error{Code: CodeCacheNewer}):
				return "", err
			case errors.Is(err, &errs.Error{Code: CodeCacheCorrupt}):
				movedAside = path + ".corrupt"
				if err := os.Rename(path, movedAside); err != nil {
					return "", errs.Wrap(err, errs.KindOf(err), "Could not move the unusable saved services for "+s.Address+" aside.").
						WithCode(CodeCacheIO).WithDetail("path", path)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return movedAside, errs.Wrap(err, errs.KindOf(err), "Could not save the services of "+s.Address+".").
			WithCode(CodeCacheIO).WithDetail("path", path)
	}
	cf := cacheFile{
		Kind:       cacheKind,
		Version:    cacheVersion,
		Address:    NormalizeAddress(s.Address),
		APIVersion: s.Version,
		FetchedAt:  s.FetchedAt.UTC(),
		Services:   s.Services,
		Files:      []cacheFileRef{},
		Problems:   []cacheProblem{},
	}
	if cf.Services == nil {
		cf.Services = []string{}
	}
	for _, f := range s.Files {
		cf.Files = append(cf.Files, cacheFileRef(f))
	}
	for _, p := range s.Problems {
		cf.Problems = append(cf.Problems, cacheProblem(p))
	}
	if err := atomicfile.WriteJSON(path, cf, 0o600); err != nil {
		return movedAside, errs.Wrap(err, errs.IO, "Could not save the services of "+s.Address+".").
			WithCode(CodeCacheIO).WithDetail("path", path)
	}
	return movedAside, nil
}
