package linker

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// TestProject links testdata/project from disk: three import roots (api,
// third_party with Google-API-style files, vendor with a proto2 library),
// the built-in well-known types, public imports, custom options and
// extensions across roots. It must link without any diagnostic.
func TestProject(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "project"))
	if err != nil {
		t.Fatal(err)
	}
	api, thirdParty, vendor := filepath.Join(dir, "api"), filepath.Join(dir, "third_party"), filepath.Join(dir, "vendor")
	res, err := Load(context.Background(), Options{
		// One input by file path, one by import path.
		Files:       []string{filepath.Join(api, "shop", "v1", "orders.proto"), "shop/v1/legacy_ext.proto"},
		ImportPaths: []string{api, thirdParty, vendor},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		t.Errorf("%s: %v", d.Code, d)
	}
	if err := res.Err(); err != nil {
		t.Errorf("Err() = %v", err)
	}
	if !slices.Equal(res.ImportPaths, []string{api, thirdParty, vendor}) {
		t.Errorf("ImportPaths = %q", res.ImportPaths)
	}
	if len(res.Files) != 2 || res.Files[0].Path != "shop/v1/orders.proto" || res.Files[1].Path != "shop/v1/legacy_ext.proto" {
		t.Fatalf("Files = %v", res.Files)
	}
	p := res.Pool
	orders := res.Files[0]
	if orders.Root != api || orders.DiskPath != filepath.Join(api, "shop", "v1", "orders.proto") || orders.Builtin {
		t.Errorf("orders.proto: Root %q DiskPath %q", orders.Root, orders.DiskPath)
	}

	// Load order: dependencies first; descriptor.proto built in.
	pos := map[string]int{}
	for i, f := range p.Files() {
		pos[f.Path] = i
	}
	for _, dep := range [][2]string{
		{"google/api/http.proto", "google/api/annotations.proto"},
		{"google/type/money.proto", "shop/v1/common.proto"},
		{"shop/v1/common.proto", "shop/v1/orders.proto"},
		{"legacy/catalog.proto", "shop/v1/legacy_ext.proto"},
	} {
		if pos[dep[0]] >= pos[dep[1]] {
			t.Errorf("%s is not before %s", dep[0], dep[1])
		}
	}
	for path, want := range map[string]string{
		"google/protobuf/descriptor.proto": "",
		"google/protobuf/timestamp.proto":  "",
		"google/api/http.proto":            thirdParty,
		"legacy/catalog.proto":             vendor,
		"shop/options/options.proto":       api,
	} {
		f := p.FindFile(path)
		if f == nil || f.Root != want || f.Builtin != (want == "") {
			t.Errorf("%s: %+v", path, f)
		}
	}

	// Types across files, packages and roots.
	order := p.FindMessage("shop.v1.Order")
	if order == nil {
		t.Fatal("shop.v1.Order not found")
	}
	if order.Comments.Leading != " Order is a customer order.\n" {
		t.Errorf("Order comments = %q", order.Comments.Leading)
	}
	type fieldWant struct {
		name        string
		kind        desc.Kind
		card        desc.Cardinality
		typeName    string
		json        string
		presence    bool
		oneof       string
		typeBuiltin bool
	}
	for _, w := range []fieldWant{
		{"name", desc.KindString, desc.Optional, "", "name", false, "", false},
		{"status", desc.KindEnum, desc.Optional, "shop.v1.Status", "status", false, "", false},
		{"total", desc.KindMessage, desc.Optional, "google.type.Money", "total", true, "", false},
		{"create_time", desc.KindMessage, desc.Optional, "google.protobuf.Timestamp", "createTime", true, "", true},
		{"delivery_date", desc.KindMessage, desc.Optional, "google.type.Date", "deliveryDate", true, "", false},
		{"lines", desc.KindMessage, desc.Repeated, "shop.v1.Order.LinesEntry", "lines", false, "", false},
		{"note", desc.KindString, desc.Optional, "", "note", true, "_note", false},
		{"email", desc.KindString, desc.Optional, "", "contact", false, "", false},
		{"address", desc.KindMessage, desc.Optional, "shop.v1.Address", "address", true, "destination", false},
		{"pickup_point", desc.KindString, desc.Optional, "", "pickupPoint", true, "destination", false},
	} {
		x := order.FieldByName(w.name)
		if x == nil {
			t.Errorf("field %s missing", w.name)
			continue
		}
		var typeName string
		var typeFile *desc.File
		switch {
		case x.Message != nil:
			typeName, typeFile = x.Message.FullName, x.Message.File
		case x.Enum != nil:
			typeName, typeFile = x.Enum.FullName, x.Enum.File
		}
		oneof := ""
		if x.Oneof != nil {
			oneof = x.Oneof.Name
		}
		if x.Kind != w.kind || x.Cardinality != w.card || typeName != w.typeName || x.TypeName != w.typeName ||
			x.JSONName != w.json || x.HasPresence != w.presence || oneof != w.oneof ||
			typeFile != nil && typeFile.Builtin != w.typeBuiltin {
			t.Errorf("field %s: kind %v card %v type %q json %q presence %v oneof %q", w.name, x.Kind, x.Cardinality,
				typeName, x.JSONName, x.HasPresence, oneof)
		}
	}
	if x := order.FieldByName("note"); !x.Proto3Optional || !x.Oneof.Synthetic || order.Oneofs[len(order.Oneofs)-1] != x.Oneof {
		t.Errorf("note: synthetic oneof not last")
	}
	lines := order.FieldByName("lines")
	if !lines.IsMap() || lines.MapKey().Kind != desc.KindString || lines.MapValue().Message != p.FindMessage("shop.v1.Order.Line") {
		t.Errorf("lines is not map<string, Line>")
	}
	if line := p.FindMessage("shop.v1.Order.Line"); line.Parent != order || line.FieldByName("status").Enum != p.FindEnum("shop.v1.Status") ||
		line.FieldByName("price").Message != p.FindMessage("google.type.Money") {
		t.Errorf("Order.Line fields do not resolve")
	}
	if p.Find(".shop.v1.PENDING").(*desc.EnumValue).Enum != p.FindEnum("shop.v1.Status") {
		t.Errorf("enum value scoping")
	}
	if x := p.FindMessage("shop.v1.WatchOrdersRequest").FieldByName("statuses"); !x.Packed {
		t.Errorf("proto3 repeated enum is not packed")
	}

	// Services.
	svc := p.FindService("shop.v1.OrderService")
	if svc == nil || len(svc.Methods) != 6 || len(p.Services()) != 1 {
		t.Fatalf("OrderService = %+v", svc)
	}
	for _, w := range []struct {
		name, in, out  string
		client, server bool
	}{
		{"GetOrder", "shop.v1.GetOrderRequest", "shop.v1.Order", false, false},
		{"DeleteOrder", "shop.v1.GetOrderRequest", "google.protobuf.Empty", false, false},
		{"WatchOrders", "shop.v1.WatchOrdersRequest", "shop.v1.Order", false, true},
		{"ImportOrders", "shop.v1.Order", "google.protobuf.Empty", true, false},
		{"Sync", "shop.v1.Order", "shop.v1.Order", true, true},
	} {
		m := svc.MethodByName(w.name)
		if m == nil || m.Input == nil || m.Output == nil || m.Input.FullName != w.in || m.Output.FullName != w.out ||
			m.ClientStreaming != w.client || m.ServerStreaming != w.server {
			t.Errorf("method %s = %+v", w.name, m)
		}
	}

	// Options.
	get := svc.MethodByName("GetOrder")
	if len(get.Options) != 1 || get.Options[0].Field() != p.FindExtension("google.api.http") || get.Options[0].Scalar != nil {
		t.Errorf("GetOrder options = %+v", get.Options)
	} else if _, ok := get.Options[0].Value.(*ast.MessageLit); !ok {
		t.Errorf("http option value is %T", get.Options[0].Value)
	}
	del := svc.MethodByName("DeleteOrder")
	if len(del.Options) != 2 || len(del.Options[1].Path) != 2 || del.Options[1].Path[1].Name != "level" ||
		del.Options[1].Scalar.Enum != p.Find("shop.options.LOW") {
		t.Errorf("DeleteOrder options = %+v", del.Options)
	}
	email := order.FieldByName("email")
	if len(email.Options) != 2 || email.Options[0].Scalar == nil || !email.Options[0].Scalar.Bool || email.Options[1].Name != "json_name" {
		t.Errorf("email options = %+v", email.Options)
	}
	if o := order.FieldByName("note").Options; len(o) != 1 || o[0].Field().FullName != "shop.options.max_length" || o[0].Scalar.Int != 280 {
		t.Errorf("note options = %+v", o)
	}
	if o := orders.Options; len(o) != 2 || o[0].Scalar.String != "orders-team" || o[1].Field().Parent.FullName != "google.protobuf.FileOptions" {
		t.Errorf("file options = %+v", o)
	}

	// Extensions across roots, proto2 semantics.
	item := p.FindMessage("legacy.catalog.Item")
	exts := p.Extensions(item)
	if len(exts) != 3 || exts[0].FullName != "shop.legacyext.order_note" || exts[2].FullName != "shop.legacyext.Holder.holder" ||
		exts[2].Message != p.FindMessage("shop.legacyext.Holder") || exts[2].Extendee != item || !exts[2].HasPresence {
		t.Errorf("extensions of Item = %v", exts)
	}
	if x := p.FindExtensionByNumber(item, 151); x == nil || x.Cardinality != desc.Repeated || x.Packed {
		t.Errorf("order_ids = %+v", x)
	}
	if x := item.FieldByName("sku"); x.Cardinality != desc.Required || !x.HasPresence {
		t.Errorf("sku = %+v", x)
	}
	if x := item.FieldByName("quantity"); x.Default == nil || x.Default.Int != 1 {
		t.Errorf("quantity default = %+v", x.Default)
	}
	if x := item.FieldByName("kind"); x.Default == nil || x.Default.Enum != p.Find("legacy.catalog.Item.PHYSICAL") || !x.Enum.Closed {
		t.Errorf("kind default = %+v", x.Default)
	}
	if x := item.FieldByName("bins"); !x.Packed {
		t.Errorf("bins is not packed")
	}
	dims := item.FieldByName("dimensions")
	if dims == nil || dims.Kind != desc.KindGroup || dims.Message != p.FindMessage("legacy.catalog.Item.Dimensions") ||
		dims.Message.FieldByName("width").Default == nil {
		t.Errorf("dimensions = %+v", dims)
	}
}
