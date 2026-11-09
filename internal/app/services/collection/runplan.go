package collection

import (
	"context"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeNotFolder: a folder run (RUN-01) was asked for an item that is not a
// folder (kind invalid_argument). Details: id.
const CodeNotFolder = "collection.not_folder"

// RunPlan returns the plan of a run of folder id (RUN-01, "" for the whole
// project): every item inside it, subfolders included, in the order the
// tree shows them, the items of a subfolder at its place. HTTP, GraphQL
// and SSE requests are run; the others are in the plan with the reason
// they are skipped: other kinds (runner.SkipKind), GraphQL subscriptions
// and operations over WebSocket (runner.SkipSubscription, read from the
// pending edit when there is one), and files of an unknown kind or that
// did not load. It is not a method, so it is not bound: the runner service
// calls it.
//
// It fails with collection.not_found for an ID that is not in the open
// project and CodeNotFolder for a request.
func RunPlan(s *CollectionService, ctx context.Context, id string) (runner.Plan, error) {
	if err := interrupted(ctx, "Listing the requests to run"); err != nil {
		return runner.Plan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return runner.Plan{}, err
	}
	folder := tree.Root()
	if id != "" {
		folder = tree.Node(id)
	}
	switch {
	case folder == nil:
		return runner.Plan{}, errs.New(errs.NotFound, "The folder is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	case !folder.IsContainer():
		return runner.Plan{}, errs.New(errs.InvalidArgument, "Only a folder can be run.").
			WithCode(CodeNotFolder).
			WithDetail("id", id)
	}
	plan := runner.Plan{Project: ws.Name()}
	if id != "" {
		plan.Folder = folder.DisplayName()
	}
	var visit func(n *core.Node, path []string, depth int)
	visit = func(n *core.Node, path []string, depth int) {
		for _, c := range n.Children {
			if c.Kind == core.KindFolder {
				// Folders nest at most core.MaxDepth deep.
				if depth < core.MaxDepth {
					visit(c, append(path[:len(path):len(path)], c.DisplayName()), depth+1)
				}
				continue
			}
			file := c.File
			if p, ok := s.saver.Get(saveKey{tree, c.ID}); ok {
				file = p.file
			}
			plan.Items = append(plan.Items, runItem(c, file, path))
		}
	}
	visit(folder, nil, 0)
	return plan, nil
}

// runItem is item n of a plan, whose file (with its pending edit) is f,
// inside the folders path.
func runItem(n *core.Node, f projectfile.File, path []string) runner.Item {
	it := runner.Item{ID: n.ID, Name: n.DisplayName(), Kind: string(n.FileKind), Path: path}
	switch n.Kind {
	case core.KindUnknown:
		it.Skip = runner.SkipUnknown
		return it
	case core.KindInvalid:
		it.Skip = runner.SkipInvalid
		return it
	}
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		it.Method = r.Method
	case *projectfile.SSERequest:
		it.Method = r.HTTP().Method
	case *projectfile.GraphQLRequest:
		it.Method = "POST"
		if r.Method != "" {
			it.Method = r.Method
		}
		// A subscription, or an operation sent over WebSocket, is a
		// connection that does not end with one response. An invalid
		// transport is left to the send, which reports it.
		transport, opType, err := request.GraphQLTransportOf(*r)
		if err == nil && (opType == "subscription" || transport == projectfile.GraphQLTransportWS || transport == projectfile.GraphQLTransportLegacyWS) {
			it.Skip = runner.SkipSubscription
		}
	default:
		it.Skip = runner.SkipKind
	}
	return it
}
