package reflection

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/descproto"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

// Problem codes added when linking or caching.
const (
	// CodeServiceMissing: a listed service is not defined by the fetched
	// files.
	CodeServiceMissing = "grpcreflection.service_missing"
	// CodeCacheNotSaved: the fetch worked but the cache could not be
	// written (the error message is in the problem).
	CodeCacheNotSaved = "grpcreflection.cache_not_saved"
	// CodeCacheReplaced: an unusable cache file was moved aside and
	// replaced.
	CodeCacheReplaced = "grpcreflection.cache_replaced"
	// CodeOffline: Resolve could not reach the server and used the cache.
	CodeOffline = "grpcreflection.offline"
)

// Schema is a linked reflection snapshot, ready for a method picker and
// the codec.
type Schema struct {
	// Snapshot is what was fetched or loaded from the cache.
	Snapshot *Snapshot
	// FromCache is true when the snapshot came from the cache.
	FromCache bool
	// Pool holds every linked file; descriptors from it work with package
	// dynamic like those of .proto files.
	Pool *desc.Pool
	// Services are the listed services found in Pool, in the server's
	// order.
	Services []*desc.Service
	// Diagnostics are descproto.Link's and the linker's.
	Diagnostics []linker.Diagnostic
	// Problems are the snapshot's problems, listed services that are not
	// defined, and cache issues.
	Problems []Problem
}

// Link links a snapshot's descriptors. The error is only for a cancelled
// context; descriptor problems are Diagnostics.
func Link(ctx context.Context, s *Snapshot) (*Schema, error) {
	res, err := descproto.Link(ctx, s.Descriptors())
	if err != nil {
		return nil, err
	}
	sc := &Schema{
		Snapshot:    s,
		Pool:        res.Pool,
		Diagnostics: res.Diagnostics,
		Problems:    append([]Problem(nil), s.Problems...),
	}
	for _, name := range s.Services {
		if svc := res.Pool.FindService(name); svc != nil {
			sc.Services = append(sc.Services, svc)
			continue
		}
		reported := false
		for _, p := range s.Problems {
			reported = reported || p.Subject == name
		}
		if !reported {
			sc.Problems = append(sc.Problems, Problem{
				Code:    CodeServiceMissing,
				Subject: name,
				Message: "The server listed " + name + " but its definition could not be loaded.",
			})
		}
	}
	return sc, nil
}

// FindMethod returns the method named "/pkg.Service/Method" (the gRPC
// path) or "pkg.Service.Method", or nil.
func (s *Schema) FindMethod(name string) *desc.Method {
	name = strings.TrimPrefix(name, "/")
	if svc, m, ok := strings.Cut(name, "/"); ok {
		name = svc + "." + m
	}
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return nil
	}
	svc := s.Pool.FindService(name[:i])
	if svc == nil {
		return nil
	}
	return svc.MethodByName(name[i+1:])
}

// Resolver loads schemas by reflection and keeps them in the project's
// cache. It is safe for concurrent use; two refreshes of one address at
// the same time both write the cache, and the last one wins.
type Resolver struct {
	Client *Client
	Cache  *Cache
}

// NewResolver returns a resolver that calls servers through inv and
// caches under the directory localDir returns.
func NewResolver(inv Invoker, localDir func() (string, error)) *Resolver {
	return &Resolver{Client: NewClient(inv), Cache: NewCache(localDir)}
}

// Refresh fetches the schema from the server, links it and replaces the
// cached snapshot. Failing to write the cache is a Problem, not an error.
func (r *Resolver) Refresh(ctx context.Context, o FetchOptions) (*Schema, error) {
	snap, err := r.Client.Fetch(ctx, o)
	if err != nil {
		return nil, err
	}
	sc, err := Link(ctx, snap)
	if err != nil {
		return nil, err
	}
	moved, err := r.Cache.Save(snap)
	if moved != "" {
		sc.Problems = append(sc.Problems, Problem{
			Code:    CodeCacheReplaced,
			Subject: moved,
			Message: "The saved services could not be read; the old file was kept as " + moved + ".",
		})
	}
	if err != nil {
		sc.Problems = append(sc.Problems, Problem{Code: CodeCacheNotSaved, Subject: o.Call.Address, Message: errText(err)})
	}
	return sc, nil
}

// LoadCached links the cached snapshot of address without any network
// access. It returns the Cache.Load errors (not cached, corrupt, newer).
func (r *Resolver) LoadCached(ctx context.Context, address string) (*Schema, error) {
	snap, err := r.Cache.Load(address)
	if err != nil {
		return nil, err
	}
	r.Client.RememberVersion(address, snap.Version)
	sc, err := Link(ctx, snap)
	if err != nil {
		return nil, err
	}
	sc.FromCache = true
	return sc, nil
}

// Resolve refreshes from the server and, if that fails for any reason but
// cancellation or invalid options, falls back to the cache with a
// CodeOffline problem. Without a usable cache the fetch error is returned.
func (r *Resolver) Resolve(ctx context.Context, o FetchOptions) (*Schema, error) {
	sc, err := r.Refresh(ctx, o)
	if err == nil {
		return sc, nil
	}
	switch errs.KindOf(err) {
	case errs.Cancelled, errs.InvalidArgument:
		return nil, err
	}
	cached, cacheErr := r.LoadCached(ctx, o.Call.Address)
	if cacheErr != nil {
		return nil, err
	}
	cached.Problems = append(cached.Problems, Problem{
		Code:    CodeOffline,
		Subject: o.Call.Address,
		Message: "Using the services saved on " + cached.Snapshot.FetchedAt.Local().Format(time.DateTime) + ": " + errText(err),
	})
	return cached, nil
}

// errText is the user-facing message of err.
func errText(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Message
	}
	return err.Error()
}
