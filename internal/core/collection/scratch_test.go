package collection

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func scratchIDs(items []ScratchItem) []string {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.Name()+"="+it.ID)
	}
	return ids
}

func TestScratch(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "scratch")
	s := NewScratch(dir)

	items, problems := s.List()
	if len(items) != 0 || len(problems) != 0 {
		t.Fatalf("empty: %v %v", items, problems)
	}
	a := must(s.Create(projectfile.KindHTTP, ""))(t)
	b := must(s.Create(projectfile.KindWebSocket, "beta"))(t)
	c := must(s.Create(projectfile.KindGRPC, ""))(t)
	idA, idB, idC := RequestFileID(a), RequestFileID(b), RequestFileID(c)
	if !IsScratchID(idA) || !s.Has(idA) || s.Has("req_0000000000000000") || s.Has("../x") {
		t.Fatalf("ids: %q", idA)
	}
	items, problems = s.List()
	want := []string{"beta=" + idB, "Scratch 1=" + idA, "Scratch 2=" + idC}
	if got := scratchIDs(items); !slices.Equal(got, want) || len(problems) != 0 {
		t.Errorf("list = %v, want %v (problems %v)", got, want, problems)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", info, err)
	}

	// Save writes only a change, and never recreates a deleted request.
	path := filepath.Join(dir, idA+".json")
	before, _ := os.Stat(path)
	noErr(t, s.Save(idA, a))
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged save wrote the file")
	}
	edited := *a.(*projectfile.HTTPRequest)
	edited.URL = "https://example.com"
	noErr(t, s.Save(idA, &edited))
	if got := must(s.Get(idA))(t).File.(*projectfile.HTTPRequest).URL; got != "https://example.com" {
		t.Errorf("url = %q", got)
	}
	checkErr(t, s.Save(idA, b), errs.InvalidArgument, CodeWrongNode)
	noErr(t, s.Delete(idA))
	noErr(t, s.Delete(idA))
	checkErr(t, s.Save(idA, &edited), errs.NotFound, CodeNotFound)
	_, err = s.Get(idA)
	checkErr(t, err, errs.NotFound, CodeNotFound)
	_, err = s.Create("folder", "x")
	checkErr(t, err, errs.InvalidArgument, CodeInvalidKind)

	// The freed name is used again; broken and foreign files are reported or ignored.
	d := must(s.Create(projectfile.KindTCP, ""))(t)
	if got := ScratchName(d); got != "Scratch 1" {
		t.Errorf("next name = %q", got)
	}
	writeFile(t, dir, "req_00000000000000bb.json", `{"kind": "http", `)
	writeFile(t, dir, "notes.json", `{"kind": "http", "id": "req_x", "method": "GET"}`)
	writeFile(t, dir, "req_00000000000000cc.json", `{"kind": "folder", "id": "fld_x"}`)
	writeFile(t, dir, "req_00000000000000dd.json", `{"schemaVersion": 99, "kind": "http", "id": "req_other", "name": "Newer", "method": "GET"}`)
	items, problems = s.List()
	if len(items) != 4 || len(problems) != 2 {
		t.Fatalf("items %v, problems %v", scratchIDs(items), problems)
	}
	newer := must(s.Get("req_00000000000000dd"))(t)
	if newer.ReadOnly != ReadOnlyNewerVersion || RequestFileID(newer.File) != "req_00000000000000dd" {
		t.Errorf("newer = %+v", newer)
	}
	checkErr(t, s.CheckWritable("req_00000000000000dd"), errs.Unsupported, CodeReadOnly)
	noErr(t, s.CheckWritable(idB))
}

func TestAddRequest(t *testing.T) {
	t.Parallel()
	tree, _ := loadSample(t)
	s := NewScratch(filepath.Join(tempDir(t), "scratch"))
	f := must(s.Create(projectfile.KindGraphQL, "Scratch 1"))(t)
	id := RequestFileID(f)

	got := must(tree.AddRequest(idOnCall, f, "Who is on call", End))(t)
	if got != id {
		t.Errorf("id = %q, want %q", got, id)
	}
	n := checkNode(t, tree, id, "users/admins/on-call/who-is-on-call.json", "Who is on call")
	if _, ok := n.File.(*projectfile.GraphQLRequest); !ok {
		t.Errorf("file = %T", n.File)
	}
	checkTree(t, tree, idOnCall)
	if ScratchName(f) != "Scratch 1" {
		t.Error("AddRequest changed its argument")
	}

	// An ID the tree has already gets a new one.
	again := must(tree.AddRequest("", f, "Again", End))(t)
	if again == id || tree.Node(again) == nil {
		t.Errorf("second id = %q", again)
	}
	checkTree(t, tree, "")

	_, err := tree.AddRequest("", &projectfile.Folder{ID: "fld_x"}, "X", End)
	checkErr(t, err, errs.InvalidArgument, CodeInvalidKind)
	_, err = tree.AddRequest("fld_missing", f, "X", End)
	checkErr(t, err, errs.NotFound, CodeNotFound)
}
