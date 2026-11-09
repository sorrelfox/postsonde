package collection

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// IDs of the sample project.
const (
	idUsers      = "fld_0b8d2e61c4a9f375"
	idAdmins     = "fld_5e2c9a7d31b0f486"
	idOnCall     = "fld_a14d8f02e6c95b37"
	idOrders     = "fld_3c7a1e9d5b2f0864"
	idHealth     = "req_5a0f93c7e2b14d68"
	idGemini     = "req_e0b1c2d3a4f56789"
	idCreateUser = "req_c93e1a7b40f2d856"
	idUserByID   = "req_e4a7c2019b5d83f6"
	idListAdmins = "req_7b3f0e5a9c2d4168"
	idAllOrders  = "req_9d0a6c3e1f7b2584"
	idCancel     = "req_4f8e2b6a0d1c9375"
)

// loadSample copies the sample project and loads it.
func loadSample(t *testing.T) (*Tree, string) {
	t.Helper()
	dir := copySample(t)
	tree, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	return tree, dir
}

// checkTree checks that tree is what loading its directory again gives,
// that loading reports no ID problems, and that the order member of each
// container (by ID, "" for the root) lists exactly its items in order.
func checkTree(t *testing.T, tree *Tree, containers ...string) {
	t.Helper()
	fresh, problems := Load(tree.Dir())
	if got, want := dump(tree, nil), dump(fresh, nil); got != want {
		t.Errorf("tree after the operation:\n%s\nloaded again:\n%s", got, want)
	}
	for _, p := range problems {
		if p.Err.Code == CodeDuplicateID || p.Err.Code == CodeMissingID {
			t.Errorf("load problem %s: %v", p.Path, p.Err)
		}
	}
	for _, id := range containers {
		n := fresh.Root()
		if id != "" {
			n = fresh.Node(id)
		}
		if n == nil {
			t.Errorf("container %s not found", id)
			continue
		}
		if got, want := n.Order(), entries(n.Children, nil); !slices.Equal(got, want) {
			t.Errorf("order of %q = %q, want %q", n.Path, got, want)
		}
	}
}

// childNames returns the display names of a container's items ("" for the
// root).
func childNames(t *testing.T, tree *Tree, id string) []string {
	t.Helper()
	n := tree.Root()
	if id != "" {
		n = tree.Node(id)
	}
	if n == nil {
		t.Fatalf("node %s not found", id)
	}
	var names []string
	for _, c := range n.Children {
		names = append(names, c.DisplayName())
	}
	return names
}

func checkNames(t *testing.T, tree *Tree, id string, want ...string) {
	t.Helper()
	if got := childNames(t, tree, id); !slices.Equal(got, want) {
		t.Errorf("items of %q = %q, want %q", id, got, want)
	}
}

func checkNode(t *testing.T, tree *Tree, id, path, name string) *Node {
	t.Helper()
	n := tree.Node(id)
	if n == nil {
		t.Fatalf("node %s not found", id)
	}
	if n.Path != path || n.DisplayName() != name {
		t.Errorf("node %s = path %q name %q, want %q %q", id, n.Path, n.DisplayName(), path, name)
	}
	return n
}

// contents maps every file below root to its content hash, ignoring
// modification times.
func contents(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffContents(before, after map[string]string) []string {
	var diffs []string
	for k, v := range before {
		if after[k] != v {
			diffs = append(diffs, k+" changed or removed")
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, k+" added")
		}
	}
	slices.Sort(diffs)
	return diffs
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// must returns v, failing the test if err is set: must(f())(t).
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)

	// A folder at the top level, second.
	folderID := must(tree.CreateFolder("", "Payments & Refunds", 1))(t)
	if !strings.HasPrefix(folderID, projectfile.FolderIDPrefix) {
		t.Errorf("folder ID = %q", folderID)
	}
	checkNode(t, tree, folderID, "payments-refunds", "Payments & Refunds")
	checkNames(t, tree, "", "Users", "Payments & Refunds", "Health check", "Echo", "Orders")
	checkTree(t, tree, "")

	// Requests in it, appended and inserted first.
	httpID := must(tree.CreateRequest(folderID, "Refund", projectfile.KindHTTP, End))(t)
	gqlID := must(tree.CreateRequest(folderID, "Payments", projectfile.KindGraphQL, 0))(t)
	checkNames(t, tree, folderID, "Payments", "Refund")
	checkNode(t, tree, httpID, "payments-refunds/refund.json", "Refund")
	checkNode(t, tree, gqlID, "payments-refunds/payments.json", "Payments")
	if f, ok := tree.Node(httpID).File.(*projectfile.HTTPRequest); !ok || f.Method != "GET" || f.ID != httpID {
		t.Errorf("new http request = %+v", tree.Node(httpID).File)
	}
	if _, ok := tree.Node(gqlID).File.(*projectfile.GraphQLRequest); !ok {
		t.Errorf("new graphql request = %+v", tree.Node(gqlID).File)
	}
	checkTree(t, tree, "", folderID)

	// Nested deep, and name collisions (ignoring case) at the top level.
	pageID := must(tree.CreateRequest(idOnCall, "Page again", projectfile.KindHTTP, 99))(t)
	checkNode(t, tree, pageID, "users/admins/on-call/page-again.json", "Page again")
	usersID := must(tree.CreateFolder("", "USERS", End))(t)
	checkNode(t, tree, usersID, "users-2", "USERS")
	healthID := must(tree.CreateRequest(tree.Root().ID, "Health", projectfile.KindHTTP, End))(t)
	checkNode(t, tree, healthID, "health-2.json", "Health")
	envID := must(tree.CreateFolder("", "Environments", End))(t)
	checkNode(t, tree, envID, "environments-2", "Environments")
	checkTree(t, tree, "", idOnCall)
	if _, err := os.Stat(filepath.Join(dir, "users-2", projectfile.FolderFileName)); err != nil {
		t.Error(err)
	}

	// Errors.
	_, err := tree.CreateRequest("", "Stream", "mqtt", End)
	checkErr(t, err, errs.InvalidArgument, CodeInvalidKind)
	_, err = tree.CreateFolder("fld_missing", "X", End)
	checkErr(t, err, errs.NotFound, CodeNotFound)
	_, err = tree.CreateFolder(idHealth, "X", End)
	checkErr(t, err, errs.InvalidArgument, CodeInvalidTarget)
}

// TestCreateTCPRequest creates a TCP request, reloads the project and
// duplicates, renames and saves it like any other request.
func TestCreateTCPRequest(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	id := must(tree.CreateRequest("", "Echo TCP", projectfile.KindTCP, End))(t)
	n := checkNode(t, tree, id, "echo-tcp.json", "Echo TCP")
	if n.FileKind != projectfile.KindTCP || n.ReadOnly != Writable {
		t.Errorf("node = %+v", n)
	}
	if got := readFile(t, dir, "echo-tcp.json"); !strings.Contains(got, `"kind": "tcp"`) {
		t.Errorf("file = %s", got)
	}

	f := *n.File.(*projectfile.TCPRequest)
	f.Address, f.Message = "localhost:7000", "hello"
	if written, err := tree.SaveRequest(id, &f); err != nil || !written {
		t.Fatalf("SaveRequest = %v, %v", written, err)
	}
	copyID := must(tree.Duplicate(id))(t)
	if c, ok := tree.Node(copyID).File.(*projectfile.TCPRequest); !ok || c.Address != "localhost:7000" || c.ID != copyID {
		t.Errorf("copy = %+v", tree.Node(copyID).File)
	}
	noErr(t, tree.Rename(id, "Echo"))
	if r := tree.Node(id).File.(*projectfile.TCPRequest); r.Name != "Echo" || r.Message != "hello" {
		t.Errorf("renamed = %+v", r)
	}

	reloaded, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if r, ok := reloaded.Node(id).File.(*projectfile.TCPRequest); !ok || r.Address != "localhost:7000" {
		t.Errorf("reloaded = %+v", reloaded.Node(id))
	}
}

// TestCreateWebSocketRequest creates a WebSocket request, saves, duplicates
// and reloads it like any other request.
func TestCreateWebSocketRequest(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	id := must(tree.CreateRequest("", "Feed", projectfile.KindWebSocket, End))(t)
	n := checkNode(t, tree, id, "feed.json", "Feed")
	if n.FileKind != projectfile.KindWebSocket || n.ReadOnly != Writable {
		t.Errorf("node = %+v", n)
	}
	f := *n.File.(*projectfile.WebSocketRequest)
	f.URL, f.Subprotocols = "ws://localhost:8080/feed", []string{"chat"}
	if written, err := tree.SaveRequest(id, &f); err != nil || !written {
		t.Fatalf("SaveRequest = %v, %v", written, err)
	}
	copyID := must(tree.Duplicate(id))(t)
	if c, ok := tree.Node(copyID).File.(*projectfile.WebSocketRequest); !ok || c.URL != f.URL || c.ID != copyID {
		t.Errorf("copy = %+v", tree.Node(copyID).File)
	}
	noErr(t, tree.Rename(id, "Live feed"))
	reloaded, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if r, ok := reloaded.Node(id).File.(*projectfile.WebSocketRequest); !ok || r.Name != "Live feed" || len(r.Subprotocols) != 1 {
		t.Errorf("reloaded = %+v", reloaded.Node(id))
	}
}

// TestCreateGRPCRequest creates a gRPC request, saves, duplicates and
// reloads it like any other request.
func TestCreateGRPCRequest(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	id := must(tree.CreateRequest("", "Get order", projectfile.KindGRPC, End))(t)
	n := checkNode(t, tree, id, "get-order.json", "Get order")
	if n.FileKind != projectfile.KindGRPC || n.ReadOnly != Writable {
		t.Errorf("node = %+v", n)
	}
	f := *n.File.(*projectfile.GRPCRequest)
	f.Address, f.Service, f.Method = "localhost:50051", "shop.v1.Orders", "GetOrder"
	f.Proto = projectfile.GRPCProto{Files: []string{"proto/orders.proto"}}
	if written, err := tree.SaveRequest(id, &f); err != nil || !written {
		t.Fatalf("SaveRequest = %v, %v", written, err)
	}
	copyID := must(tree.Duplicate(id))(t)
	if c, ok := tree.Node(copyID).File.(*projectfile.GRPCRequest); !ok || c.Service != f.Service || c.ID != copyID {
		t.Errorf("copy = %+v", tree.Node(copyID).File)
	}
	noErr(t, tree.Rename(id, "Order"))
	reloaded, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if r, ok := reloaded.Node(id).File.(*projectfile.GRPCRequest); !ok || r.Name != "Order" || r.Method != "GetOrder" || len(r.Proto.Files) != 1 {
		t.Errorf("reloaded = %+v", reloaded.Node(id))
	}
}

// TestCreateSSERequest creates an SSE request, saves, duplicates and
// reloads it like any other request.
func TestCreateSSERequest(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	id := must(tree.CreateRequest("", "Price stream", projectfile.KindSSE, End))(t)
	n := checkNode(t, tree, id, "price-stream.json", "Price stream")
	if n.FileKind != projectfile.KindSSE || n.ReadOnly != Writable {
		t.Errorf("node = %+v", n)
	}
	f := *n.File.(*projectfile.SSERequest)
	if f.Auth.Type != projectfile.AuthInherit || f.Method != "" {
		t.Errorf("new file = %+v", f)
	}
	f.URL = "http://localhost:8080/events"
	if written, err := tree.SaveRequest(id, &f); err != nil || !written {
		t.Fatalf("SaveRequest = %v, %v", written, err)
	}
	copyID := must(tree.Duplicate(id))(t)
	if c, ok := tree.Node(copyID).File.(*projectfile.SSERequest); !ok || c.URL != f.URL || c.ID != copyID {
		t.Errorf("copy = %+v", tree.Node(copyID).File)
	}
	noErr(t, tree.Rename(id, "Prices"))
	reloaded, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if r, ok := reloaded.Node(id).File.(*projectfile.SSERequest); !ok || r.Name != "Prices" || r.URL != f.URL {
		t.Errorf("reloaded = %+v", reloaded.Node(id))
	}
}

// TestCreateSocketIORequest creates a Socket.IO request, saves, duplicates
// and reloads it like any other request.
func TestCreateSocketIORequest(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	id := must(tree.CreateRequest("", "Chat", projectfile.KindSocketIO, End))(t)
	n := checkNode(t, tree, id, "chat.json", "Chat")
	if n.FileKind != projectfile.KindSocketIO || n.ReadOnly != Writable {
		t.Errorf("node = %+v", n)
	}
	f := *n.File.(*projectfile.SocketIORequest)
	f.URL = "http://localhost:3000"
	f.Namespace = "/admin"
	f.SavedEmits = []projectfile.SocketIOEmit{{Name: "Join", Event: "join", Args: `["lobby"]`, Ack: true}}
	if written, err := tree.SaveRequest(id, &f); err != nil || !written {
		t.Fatalf("SaveRequest = %v, %v", written, err)
	}
	copyID := must(tree.Duplicate(id))(t)
	if c, ok := tree.Node(copyID).File.(*projectfile.SocketIORequest); !ok || c.URL != f.URL || c.ID != copyID {
		t.Errorf("copy = %+v", tree.Node(copyID).File)
	}
	noErr(t, tree.Rename(id, "Lobby"))
	reloaded, problems := Load(dir)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	r, ok := reloaded.Node(id).File.(*projectfile.SocketIORequest)
	if !ok || r.Name != "Lobby" || r.Namespace != "/admin" || len(r.SavedEmits) != 1 || r.SavedEmits[0].Event != "join" {
		t.Errorf("reloaded = %+v", reloaded.Node(id))
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)

	// A request: new file name, same place and ID.
	noErr(t, tree.Rename(idCreateUser, "Add user"))
	checkNode(t, tree, idCreateUser, "users/add-user.json", "Add user")
	checkNames(t, tree, idUsers, "User by ID", "Admins", "Add user", "Hand edited", "Upload avatar")
	checkTree(t, tree, idUsers)

	// Same slug: only the name changes.
	noErr(t, tree.Rename(idCreateUser, "ADD USER"))
	checkNode(t, tree, idCreateUser, "users/add-user.json", "ADD USER")

	// A folder with nested folders: every path below follows, IDs stay.
	noErr(t, tree.Rename(idAdmins, "Administrators"))
	checkNode(t, tree, idAdmins, "users/administrators", "Administrators")
	checkNode(t, tree, idOnCall, "users/administrators/on-call", "On call")
	checkNode(t, tree, idListAdmins, "users/administrators/list-admins.json", "List admins")
	checkNames(t, tree, idUsers, "User by ID", "Administrators", "ADD USER", "Hand edited", "Upload avatar")
	checkTree(t, tree, idUsers)

	// A name whose file name is taken gets a suffix, and keeps it while
	// the plain name is still taken.
	noErr(t, tree.Rename(idCreateUser, "User by ID"))
	checkNode(t, tree, idCreateUser, "users/user-by-id-2.json", "User by ID")
	noErr(t, tree.Rename(idCreateUser, "User By Id"))
	checkNode(t, tree, idCreateUser, "users/user-by-id-2.json", "User By Id")
	checkTree(t, tree, idUsers)

	// Case-only change of the file name.
	checkNode(t, tree, idCancel, "orders/Cancel-order.json", "Cancel order")
	noErr(t, tree.Rename(idCancel, "Cancel Order"))
	checkNode(t, tree, idCancel, "orders/cancel-order.json", "Cancel Order")
	if got := dirNames(t, filepath.Join(dir, "orders")); !slices.Equal(got, []string{"_folder.json", "all-orders.json", "cancel-order.json"}) {
		t.Errorf("orders = %q", got)
	}
	checkTree(t, tree, idOrders)

	// Read-only items and missing IDs.
	checkErr(t, tree.Rename(idGemini, "Gemini"), errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Rename("req_missing", "X"), errs.NotFound, CodeNotFound)
	checkErr(t, tree.Rename(tree.Root().ID, "X"), errs.InvalidArgument, CodeWrongNode)
}

func TestRenamedEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base, slug string
		others     []string
		want       string
	}{
		{"users", "users", nil, "users.json"},
		{"users-2", "users", []string{"users.json"}, "users-2.json"},
		{"users-3", "users", nil, "users.json"},
		{"users-2024", "users", nil, "users.json"},
		{"old", "users", []string{"USERS.json"}, "users-2.json"},
	}
	for _, tt := range tests {
		if got := renamedEntry(tt.base, tt.slug, ".json", tt.others, false); got != tt.want {
			t.Errorf("renamedEntry(%q, %q, %q) = %q, want %q", tt.base, tt.slug, tt.others, got, tt.want)
		}
	}
}

func TestCopyName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		siblings []string
		want     string
	}{
		{"Health", []string{"Health"}, "Health copy"},
		{"Health", []string{"Health", "health COPY"}, "Health copy 2"},
		{"Health copy", []string{"Health", "Health copy"}, "Health copy 2"},
		{"Health copy 2", []string{"Health", "Health copy", "Health copy 2"}, "Health copy 3"},
		{"copy", []string{"copy"}, "copy copy"},
	}
	for _, tt := range tests {
		if got := copyName(tt.name, tt.siblings); got != tt.want {
			t.Errorf("copyName(%q, %q) = %q, want %q", tt.name, tt.siblings, got, tt.want)
		}
	}
}

func TestDuplicate(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)

	// A request, placed right after the original, then copies of copies.
	copyID := must(tree.Duplicate(idHealth))(t)
	checkNode(t, tree, copyID, "health-check-copy.json", "Health check copy")
	checkNames(t, tree, "", "Users", "Health check", "Health check copy", "Echo", "Orders")
	copy2 := must(tree.Duplicate(idHealth))(t)
	checkNode(t, tree, copy2, "health-check-copy-2.json", "Health check copy 2")
	copy3 := must(tree.Duplicate(copyID))(t)
	checkNode(t, tree, copy3, "health-check-copy-3.json", "Health check copy 3")
	checkNames(t, tree, "", "Users", "Health check", "Health check copy 2", "Health check copy", "Health check copy 3", "Echo", "Orders")
	orig := tree.Node(idHealth).File.(*projectfile.HTTPRequest)
	dup := tree.Node(copyID).File.(*projectfile.HTTPRequest)
	if dup.URL != orig.URL || dup.Method != orig.Method {
		t.Errorf("copy = %+v, original %+v", dup, orig)
	}
	checkTree(t, tree, "")

	// A folder with nested folders: every copied item has a new ID.
	usersBefore := contents(t, filepath.Join(dir, "users"))
	usersCopy := must(tree.Duplicate(idUsers))(t)
	checkNode(t, tree, usersCopy, "users-copy", "Users copy")
	checkNames(t, tree, "", "Users", "Users copy", "Health check", "Health check copy 2", "Health check copy", "Health check copy 3", "Echo", "Orders")
	var origIDs, copyIDs []string
	var origPaths, copyPaths []string
	walkNode(tree.Node(idUsers), func(n *Node) {
		origIDs = append(origIDs, n.ID)
		origPaths = append(origPaths, strings.TrimPrefix(n.Path, "users"))
	})
	walkNode(tree.Node(usersCopy), func(n *Node) {
		copyIDs = append(copyIDs, n.ID)
		copyPaths = append(copyPaths, strings.TrimPrefix(n.Path, "users-copy"))
		if n.IDChanged {
			t.Errorf("%s has a derived ID", n.Path)
		}
	})
	if len(copyIDs) != 9 || !slices.Equal(origPaths, copyPaths) {
		t.Errorf("copied paths = %q, want %q", copyPaths, origPaths)
	}
	for _, id := range copyIDs {
		if slices.Contains(origIDs, id) {
			t.Errorf("copy reuses ID %s", id)
		}
	}
	checkNames(t, tree, usersCopy, childNames(t, tree, idUsers)...)
	checkTree(t, tree, "", usersCopy)
	// The original is untouched, and no temporary directory is left.
	if d := diffContents(usersBefore, contents(t, filepath.Join(dir, "users"))); len(d) != 0 {
		t.Errorf("duplicating changed the original: %q", d)
	}
	for _, name := range dirNames(t, dir) {
		if strings.Contains(name, ".copy-") {
			t.Errorf("left %s", name)
		}
	}
	// Unknown members are copied.
	if !strings.Contains(readFile(t, dir, "users-copy/_folder.json"), `"color": "teal"`) {
		t.Error("the copied _folder.json lost its unknown member")
	}

	// A folder holding an item that cannot be rewritten is not copied.
	noErr(t, tree.Move(idGemini, idOrders, End))
	_, err := tree.Duplicate(idOrders)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	_, err = tree.Duplicate(idGemini)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
}

func TestMove(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)

	// Reordering within a folder.
	noErr(t, tree.Move(idHealth, "", 0))
	checkNames(t, tree, "", "Health check", "Users", "Echo", "Orders")
	checkNode(t, tree, idHealth, "health.json", "Health check")
	noErr(t, tree.Move(idHealth, tree.Root().ID, End))
	checkNames(t, tree, "", "Users", "Echo", "Orders", "Health check")
	checkTree(t, tree, "")

	// A request between folders.
	noErr(t, tree.Move(idCreateUser, idOrders, 1))
	checkNode(t, tree, idCreateUser, "orders/create-user.json", "Create user")
	checkNames(t, tree, idOrders, "List orders", "Create user", "Cancel order")
	checkNames(t, tree, idUsers, "User by ID", "Admins", "Hand edited", "Upload avatar")
	checkTree(t, tree, idOrders, idUsers)

	// To the top level, where its file name is taken: only the file name
	// changes.
	must(tree.CreateRequest("", "All orders", projectfile.KindHTTP, End))(t)
	noErr(t, tree.Move(idAllOrders, "", 0))
	checkNode(t, tree, idAllOrders, "all-orders-2.json", "List orders")
	checkNames(t, tree, "", "List orders", "Users", "Echo", "Orders", "Health check", "All orders")
	checkTree(t, tree, "", idOrders)

	// A folder with its subfolders, into another folder.
	noErr(t, tree.Move(idAdmins, idOrders, End))
	checkNode(t, tree, idAdmins, "orders/admins", "Admins")
	checkNode(t, tree, idOnCall, "orders/admins/on-call", "On call")
	checkNode(t, tree, idListAdmins, "orders/admins/list-admins.json", "List admins")
	checkTree(t, tree, idOrders, idUsers)

	// An unknown item moves as a file, unchanged.
	before := readFile(t, dir, "gemini-echo.json")
	noErr(t, tree.Move(idGemini, idUsers, 0))
	checkNode(t, tree, idGemini, "users/gemini-echo.json", "Echo")
	if readFile(t, dir, "users/gemini-echo.json") != before {
		t.Error("moving rewrote the unknown file")
	}
	checkTree(t, tree, "", idUsers)

	// Not into itself or below itself, not into a request.
	checkErr(t, tree.Move(idOrders, idOrders, End), errs.InvalidArgument, CodeInvalidTarget)
	checkErr(t, tree.Move(idOrders, idOnCall, End), errs.InvalidArgument, CodeInvalidTarget)
	checkErr(t, tree.Move(idHealth, idUserByID, End), errs.InvalidArgument, CodeInvalidTarget)
	checkErr(t, tree.Move(idHealth, "fld_missing", End), errs.NotFound, CodeNotFound)
	checkErr(t, tree.Move(tree.Root().ID, idUsers, End), errs.InvalidArgument, CodeWrongNode)
}

func TestMoveSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	tree, dir := loadSample(t)
	if err := os.Symlink("health.json", filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	tree.reload()
	// The link duplicates health.json's id; it gets a derived one.
	link := tree.NodeAt("linked.json")
	if link == nil || link.ReadOnly != ReadOnlySymlink {
		t.Fatalf("link = %+v", link)
	}
	noErr(t, tree.Move(link.ID, "", 0))
	checkErr(t, tree.Move(link.ID, idUsers, End), errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Rename(link.ID, "X"), errs.Unsupported, CodeReadOnly)
}

func TestReadOnlyTargets(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))

	// A folder from a newer PostSonde: nothing goes in, but it can be
	// moved and deleted as a whole.
	writeFile(t, dir, "orders/_folder.json", `{"schemaVersion": 99, "kind": "folder", "id": "fld_3c7a1e9d5b2f0864", "name": "Orders"}`)
	tree.reload()
	_, err := tree.CreateRequest(idOrders, "X", projectfile.KindHTTP, End)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Move(idHealth, idOrders, End), errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Move(idAllOrders, "", End), errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Rename(idOrders, "X"), errs.Unsupported, CodeReadOnly)
	noErr(t, tree.Move(idOrders, idUsers, End))
	must(tree.Delete(idOrders, trash))(t)
	checkTree(t, tree, idUsers)

	// A project from a newer PostSonde: nothing at all.
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 99, "kind": "workspace", "id": "ws_7c1e4b9a2f3d5e60"}`)
	tree.reload()
	_, err = tree.CreateFolder("", "X", End)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	checkErr(t, tree.Move(idHealth, "", 0), errs.Unsupported, CodeReadOnly)
	_, err = tree.Delete(idHealth, trash)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
	_, err = tree.Duplicate(idHealth)
	checkErr(t, err, errs.Unsupported, CodeReadOnly)
}

func TestDeleteRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trashDir := filepath.Join(tempDir(t), "trash")
	trash := NewTrash(trashDir)

	treeBefore := dump(tree, nil)
	filesBefore := contents(t, dir)

	e := must(tree.Delete(idUsers, trash))(t)
	if e.NodeID != idUsers || e.Kind != KindFolder || e.Name != "Users" || e.Path != "users" || e.ParentID != "" || e.Index != 0 || e.DeletedAt.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if tree.Node(idUsers) != nil || tree.Node(idOnCall) != nil {
		t.Error("the deleted folder is still in the tree")
	}
	if _, err := os.Stat(filepath.Join(dir, "users")); !os.IsNotExist(err) {
		t.Errorf("users still on disk: %v", err)
	}
	checkTree(t, tree, "")
	list := must(trash.List())(t)
	if len(list) != 1 || list[0] != e {
		t.Errorf("List = %+v, want %+v", list, e)
	}
	if _, err := os.Stat(filepath.Join(trashDir, e.ID, TrashDataDir, "users", "admins", "on-call", "page-on-call.json")); err != nil {
		t.Error(err)
	}

	r := must(tree.Restore(trash, e.ID))(t)
	if r != (Restored{NodeID: idUsers, Path: "users"}) {
		t.Errorf("Restored = %+v", r)
	}
	if got := dump(tree, nil); got != treeBefore {
		t.Errorf("tree after restoring:\n%s\nbefore:\n%s", got, treeBefore)
	}
	checkTree(t, tree, "")
	// Every file is back byte for byte; postsonde.json now lists every
	// item in its order.
	if d := diffContents(filesBefore, contents(t, dir)); !slices.Equal(d, []string{"postsonde.json changed or removed"}) {
		t.Errorf("files changed: %q", d)
	}
	if list := must(trash.List())(t); len(list) != 0 {
		t.Errorf("trash after restoring = %+v", list)
	}
	if names, _ := os.ReadDir(trashDir); len(names) != 0 {
		t.Errorf("trash directory not empty: %v", names)
	}

	// A nested request and a top-level one: the order of their folder
	// lists every item after each step.
	for _, c := range []struct{ id, parent string }{{idListAdmins, idAdmins}, {idHealth, ""}} {
		e := must(tree.Delete(c.id, trash))(t)
		checkTree(t, tree, c.parent)
		must(tree.Restore(trash, e.ID))(t)
		checkTree(t, tree, c.parent)
		if got := dump(tree, nil); got != treeBefore {
			t.Errorf("tree after restoring %s:\n%s", c.id, got)
		}
	}

	// Once the orders list every item, a round trip leaves every file
	// identical.
	filesBefore = contents(t, dir)
	for _, id := range []string{idListAdmins, idUsers, idHealth, idOrders} {
		e := must(tree.Delete(id, trash))(t)
		must(tree.Restore(trash, e.ID))(t)
	}
	if d := diffContents(filesBefore, contents(t, dir)); len(d) != 0 {
		t.Errorf("files changed: %q", d)
	}

	// Restoring twice, or an unknown entry, fails.
	_, err := tree.Restore(trash, e.ID)
	checkErr(t, err, errs.NotFound, CodeTrashNotFound)
	_, err = tree.Restore(trash, "../users")
	checkErr(t, err, errs.NotFound, CodeTrashNotFound)
}

func TestDeleteReadOnlyItem(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))
	writeFile(t, dir, "orders/broken.json", `{"kind": "http", "id": `)
	tree.reload()
	broken := tree.NodeAt("orders/broken.json")
	if broken == nil || broken.Kind != KindInvalid {
		t.Fatalf("broken = %+v", broken)
	}
	for _, id := range []string{idGemini, broken.ID} {
		e := must(tree.Delete(id, trash))(t)
		must(tree.Restore(trash, e.ID))(t)
	}
	if readFile(t, dir, "orders/broken.json") != `{"kind": "http", "id": ` {
		t.Error("the invalid file changed")
	}
	checkTree(t, tree, "", idOrders)
}

func TestRestoreNameTaken(t *testing.T) {
	t.Parallel()
	tree, dir := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))
	before := readFile(t, dir, "health.json")

	e := must(tree.Delete(idHealth, trash))(t)
	newID := must(tree.CreateRequest("", "Health", projectfile.KindHTTP, End))(t)
	checkNode(t, tree, newID, "health.json", "Health")

	r := must(tree.Restore(trash, e.ID))(t)
	if r.NodeID != idHealth || r.Path != "health-2.json" || r.ToRoot {
		t.Errorf("Restored = %+v", r)
	}
	checkNode(t, tree, idHealth, "health-2.json", "Health check")
	checkNames(t, tree, "", "Users", "Health check", "Echo", "Orders", "Health")
	if readFile(t, dir, "health-2.json") != before {
		t.Error("the restored file changed")
	}
	checkTree(t, tree, "")
}

func TestRestoreParentGone(t *testing.T) {
	t.Parallel()
	tree, _ := loadSample(t)
	trash := NewTrash(filepath.Join(tempDir(t), "trash"))

	// Parent deleted and restored first: the item goes back inside, in
	// its place.
	item := must(tree.Delete(idListAdmins, trash))(t)
	parent := must(tree.Delete(idAdmins, trash))(t)
	must(tree.Restore(trash, parent.ID))(t)
	r := must(tree.Restore(trash, item.ID))(t)
	if r != (Restored{NodeID: idListAdmins, Path: "users/admins/list-admins.json"}) {
		t.Errorf("Restored = %+v", r)
	}
	checkNames(t, tree, idAdmins, "List admins", "On call")
	checkTree(t, tree, idUsers, idAdmins)

	// The parent follows a move.
	item = must(tree.Delete(idListAdmins, trash))(t)
	noErr(t, tree.Move(idAdmins, idOrders, 0))
	r = must(tree.Restore(trash, item.ID))(t)
	if r.Path != "orders/admins/list-admins.json" || r.ToRoot {
		t.Errorf("Restored = %+v", r)
	}

	// Parent deleted for good: the item goes to the end of the top level.
	item = must(tree.Delete(idListAdmins, trash))(t)
	parent = must(tree.Delete(idAdmins, trash))(t)
	noErr(t, os.RemoveAll(filepath.Join(trash.Dir(), parent.ID)))
	r = must(tree.Restore(trash, item.ID))(t)
	if r != (Restored{NodeID: idListAdmins, Path: "list-admins.json", ToRoot: true}) {
		t.Errorf("Restored = %+v", r)
	}
	checkNames(t, tree, "", "Users", "Health check", "Echo", "Orders", "List admins")
	checkTree(t, tree, "", idOrders)
}
