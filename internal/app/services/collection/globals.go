package collection

import (
	"context"
	"maps"
	"strings"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Global variables (ENV-02, docs/environments.md#global-variables): the
// "variables" of postsonde.json, available to every request of the
// project. They are the lowest scope of the chain (ENV-06). The tree's
// root node holds the file, so they are read from and written through the
// tree: a later write of postsonde.json (a reorder of the top level) keeps
// them. Secret ones keep their value in the workspace's side file
// (<local>/secrets/requests/<workspace id>.json, "var.<name>"), as a
// folder's secret variables do.
//
// Like LoadVariablesSource these are functions, not methods, so they are
// not bound: EnvironmentsService calls them (its global variables editor).

// GlobalVariables is the project's global variables, secret ones with the
// value stored on this computer ("" when there is none).
type GlobalVariables struct {
	Variables []projectfile.Variable
	// ReadOnly is set when postsonde.json cannot be written (a newer
	// PostSonde's file, or one that did not load).
	ReadOnly bool
}

// LoadGlobalVariables returns the global variables of the open project. It
// fails with workspace.none_open without one and
// requestsecrets.local_data_failed when the side file cannot be read.
func LoadGlobalVariables(s *CollectionService, ctx context.Context) (GlobalVariables, error) {
	if err := interrupted(ctx, "Loading the global variables"); err != nil {
		return GlobalVariables{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return GlobalVariables{}, err
	}
	vars, err := globalVariablesLocked(ws.LocalDir, tree)
	if err != nil {
		return GlobalVariables{}, err
	}
	return GlobalVariables{Variables: vars, ReadOnly: tree.ReadOnly() != core.Writable}, nil
}

// SaveGlobalVariables replaces the global variables of the open project
// with vars, in order, and writes postsonde.json at once (only when its
// content changed). Secret values go to the workspace's side file first;
// the project file gets "". Members PostSonde does not know are kept on
// each variable, matched by name. It fails with collection.read_only when
// the file cannot be written, projectfile.* codes when it cannot be
// encoded or saved, and requestsecrets.local_data_failed when the side
// file cannot be written (the project file is then left as it was).
func SaveGlobalVariables(s *CollectionService, ctx context.Context, vars []projectfile.Variable) (GlobalVariables, error) {
	if err := interrupted(ctx, "Saving the global variables"); err != nil {
		return GlobalVariables{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return GlobalVariables{}, err
	}
	root := tree.Root()
	if err := tree.CheckWritable(root.ID, core.KindRoot); err != nil {
		return GlobalVariables{}, err
	}
	current, ok := root.File.(*projectfile.Workspace)
	if !ok {
		return GlobalVariables{}, errs.New(errs.Unsupported, "The project file could not be read, so it is not overwritten.").
			WithCode(core.CodeReadOnly).
			WithHint("Fix postsonde.json by hand, then reopen the project.")
	}
	next := *current
	next.Variables = keepUnknown(current.Variables, vars)
	split, values := requestsecrets.SplitVariables(next.Variables)
	next.Variables = split

	// The side file first, keeping what it holds under other keys: a
	// failed project write leaves both usable.
	store := requestsecrets.New(ws.LocalDir)
	side, err := store.Read(root.ID)
	if err != nil {
		return GlobalVariables{}, err
	}
	want := maps.Clone(side)
	if want == nil {
		want = map[string]string{}
	}
	maps.DeleteFunc(want, func(k, _ string) bool { return strings.HasPrefix(k, requestsecrets.VarKeyPrefix) })
	maps.Copy(want, values)
	if !maps.Equal(side, want) {
		if err := store.Write(root.ID, want); err != nil {
			return GlobalVariables{}, err
		}
	}
	if _, err := tree.SaveWorkspace(&next); err != nil {
		return GlobalVariables{}, err
	}
	s.logger.Info("global variables saved", "workspace", ws.ID(), "count", len(next.Variables))
	out, err := globalVariablesLocked(ws.LocalDir, tree)
	if err != nil {
		return GlobalVariables{}, err
	}
	return GlobalVariables{Variables: out}, nil
}

// keepUnknown returns vars with the unknown members of the variable of the
// same name in old (the first one) carried over.
func keepUnknown(old, vars []projectfile.Variable) []projectfile.Variable {
	if len(vars) == 0 {
		return nil
	}
	unknown := map[string]projectfile.Unknown{}
	for _, v := range old {
		if _, seen := unknown[v.Name]; !seen && len(v.Unknown) > 0 {
			unknown[v.Name] = v.Unknown
		}
	}
	out := make([]projectfile.Variable, len(vars))
	for i, v := range vars {
		if v.Unknown == nil {
			v.Unknown = unknown[v.Name]
		}
		out[i] = v
	}
	return out
}

// globalVariablesLocked is the variables of the tree's postsonde.json,
// secret ones with their local value (a value typed into the file by hand
// when this computer has none). Never nil, so VariablesSource.Globals
// says it was read; empty when the file did not load.
func globalVariablesLocked(localDir string, tree *core.Tree) ([]projectfile.Variable, error) {
	w, ok := tree.Root().File.(*projectfile.Workspace)
	if !ok || len(w.Variables) == 0 {
		return []projectfile.Variable{}, nil
	}
	vars := w.Variables
	if requestsecrets.HasSecretVariables(vars) {
		values, err := requestsecrets.New(localDir).Read(tree.Root().ID)
		if err != nil {
			return nil, err
		}
		vars = requestsecrets.MergeVariables(vars, values)
	}
	return vars, nil
}
