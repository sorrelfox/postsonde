package xmldom

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"
)

// FuzzParse parses random input, strict and in HTML mode. Parse must not
// panic and must return one of its error types; a tree must be consistent:
// document order, parent links, sibling indexes, offsets inside Source.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`<?xml version="1.0"?><!DOCTYPE a [<!ENTITY e "x">]><a xmlns="urn:a" xmlns:p="urn:p" p:x="1">t&amp;<![CDATA[<c>]]><!--c--><?pi d?><p:b/></a>`,
		`<html><body><p class=x>One<br>Two<li>a<li>b</ul>&nbsp;&copy;</html>`,
		"\xFF\xFE<\x00a\x00/\x00>\x00",
		`<?xml version="1.0" encoding="ISO-8859-1"?><a>` + "\xe9" + `</a>`,
		`<a b="1" b="2"><c></a>`, `<!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;">]><lolz>&lol2;</lolz>`,
	} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, data []byte, html bool) {
		doc, err := ParseOptions(context.Background(), data, Options{HTML: html, Limits: Limits{MaxNodes: 20_000, MaxDepth: 200}})
		if err != nil {
			var se *SyntaxError
			var le *LimitError
			var ee *EncodingError
			if !errors.As(err, &se) && !errors.As(err, &le) && !errors.As(err, &ee) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
			if se != nil && (se.Offset < 0 || se.Line < 1 || se.Column < 1) {
				t.Fatalf("bad position %+v", se)
			}
			_ = AsErr(err).Error()
			return
		}
		next := 0
		var walk func(n *Node)
		walk = func(n *Node) {
			if n.Index != next {
				t.Fatalf("%v node has index %d, want %d", n.Type, n.Index, next)
			}
			next++
			if n.Offset < 0 || n.End < n.Offset || n.End > len(doc.Source) {
				t.Fatalf("%v node offsets %d..%d outside %d", n.Type, n.Offset, n.End, len(doc.Source))
			}
			if n.Type != DocumentNode && n.Type != DoctypeNode && n.Path() == "" {
				t.Fatalf("%v node without a path", n.Type)
			}
			if !html && !utf8.ValidString(n.Value) {
				t.Fatalf("invalid UTF-8 value %q", n.Value)
			}
			for i, a := range n.Attrs {
				if a.Parent != n || a.SiblingIndex() != i || a.Type != AttributeNode {
					t.Fatal("bad attribute link")
				}
				walk(a)
			}
			for i, c := range n.Children {
				if c.Parent != n || c.SiblingIndex() != i {
					t.Fatal("bad child link")
				}
				walk(c)
			}
		}
		walk(doc.Root)
		if next != doc.NodeCount {
			t.Fatalf("NodeCount %d, walked %d", doc.NodeCount, next)
		}
		_ = doc.Position(len(doc.Source) / 2)
	})
}
