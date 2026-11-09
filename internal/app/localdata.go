package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/app/services/graphql"
	historyservice "github.com/sorrelfox/postsonde/internal/app/services/history"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/localdata"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	corehistory "github.com/sorrelfox/postsonde/internal/core/history"
	corelocaldata "github.com/sorrelfox/postsonde/internal/core/localdata"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	coreruntimevars "github.com/sorrelfox/postsonde/internal/core/runtimevars"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// localDataDeps are the services that keep local data in memory, which
// "Clear local data" (PRV-13) tells before it removes the files.
type localDataDeps struct {
	workspaces   *coreworkspace.Manager
	history      *historyservice.HistoryService
	historyStore *corehistory.Registry
	// http keeps the last responses {{$response}} references read.
	http        *httpservice.HTTPService
	cookies     *cookies.Registry
	tokens      *tokenstore.Store
	graphql     *graphql.GraphQLService
	runtimeVars *coreruntimevars.Registry
	emit        func(name string, data any)
}

// localDataHooks are the LocalDataService hooks (docs/privacy.md#clearing-local-data):
//
//   - history: the open project's through HistoryService.Clear, which
//     tells its views (history:changed); another project's through its
//     store, so a store opened earlier in the session does not keep a
//     stale index; the responses kept for {{$response}} references are
//     dropped too;
//   - cookies: every jar of the project forgotten in memory, never saved
//     again;
//   - tokens: the open project's cleared, every token held in memory
//     dropped, and the Auth tabs told (http:oauth2-token-changed);
//   - schemas: the schemas held in memory dropped;
//   - runtime variables: the open project's, session ones included,
//     through the registry, which tells the Environments view.
//
// The run history keeps nothing in memory: its files are enough.
func localDataHooks(d localDataDeps) []localdata.Option {
	return []localdata.Option{
		localdata.WithHook(corelocaldata.History, func(ctx context.Context, dir string, open bool) error {
			// The responses {{$response}} references read (DYN-06) go
			// with the history.
			httpservice.ForgetResponses(d.http, "")
			if open {
				return d.history.Clear(ctx)
			}
			if _, err := os.Stat(filepath.Join(dir, corehistory.DirName)); errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			st, err := d.historyStore.Store(dir)
			if err != nil {
				return err
			}
			return st.Clear()
		}),
		localdata.WithHook(corelocaldata.Cookies, func(_ context.Context, dir string, _ bool) error {
			d.cookies.Forget(dir)
			return nil
		}),
		localdata.WithHook(corelocaldata.Tokens, func(_ context.Context, _ string, open bool) error {
			var err error
			if open {
				err = d.tokens.Clear()
			}
			d.tokens.Forget()
			d.emit(httpservice.EventOAuth2TokenChanged, httpservice.OAuth2TokenChangedDTO{})
			return err
		}),
		localdata.WithHook(corelocaldata.Schemas, func(context.Context, string, bool) error {
			graphql.ForgetSchemas(d.graphql)
			return nil
		}),
		localdata.WithHook(corelocaldata.RuntimeVariables, func(_ context.Context, _ string, open bool) error {
			if !open {
				return nil
			}
			return d.runtimeVars.Clear(d.workspaces.Current())
		}),
	}
}
