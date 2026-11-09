package ast

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var (
	locType      = reflect.TypeFor[Loc]()
	commentsType = reflect.TypeFor[Comments]()
	fullNameType = reflect.TypeFor[*FullName]()
	identType    = reflect.TypeFor[*Ident]()
)

// Dump returns an indented, deterministic text form of the tree rooted at n,
// one node per line with its byte range and non-zero scalar fields, for
// tests and debugging. Identifiers and full names are shown inline:
//
//	Message [0:24] RBrace=23 Leading=" A message.\n" Name=[8:11]"Foo"
//	  Body[0]: Field [14:22] Type=[14:19]"int32" Name=[20:21]"a"
//	    Number: IntLit [24:25] Raw="1" Value=1
func Dump(n Node) string {
	var b strings.Builder
	dump(&b, "", reflect.ValueOf(n), 0)
	return b.String()
}

func dump(b *strings.Builder, label string, v reflect.Value, depth int) {
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	t := v.Type()
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(label)
	b.WriteString(t.Name())

	type child struct {
		label string
		v     reflect.Value
	}
	var kids []child
	for i := range t.NumField() {
		f, fv := t.Field(i), v.Field(i)
		switch {
		case !f.IsExported() || f.Name == "Source":
		case f.Type == locType && f.Anonymous:
			fmt.Fprintf(b, " [%d:%d]", fv.Field(0).Int(), fv.Field(1).Int())
		case f.Type == locType:
			if !fv.IsZero() {
				fmt.Fprintf(b, " %s=[%d:%d]", f.Name, fv.Field(0).Int(), fv.Field(1).Int())
			}
		case f.Type == commentsType:
			c := fv.Interface().(Comments)
			if c.Leading != "" {
				fmt.Fprintf(b, " Leading=%q", c.Leading)
			}
			if c.Trailing != "" {
				fmt.Fprintf(b, " Trailing=%q", c.Trailing)
			}
			if len(c.Detached) > 0 {
				fmt.Fprintf(b, " Detached=%q", c.Detached)
			}
		case f.Type == identType:
			if !fv.IsNil() {
				id := fv.Interface().(*Ident)
				fmt.Fprintf(b, " %s=[%d:%d]%q", f.Name, id.Start, id.End, id.Value)
			}
		case f.Type == fullNameType:
			if !fv.IsNil() {
				n := fv.Interface().(*FullName)
				fmt.Fprintf(b, " %s=[%d:%d]%q", f.Name, n.Start, n.End, n.String())
			}
		case f.Type.Kind() == reflect.Slice:
			for j := range fv.Len() {
				kids = append(kids, child{f.Name + "[" + strconv.Itoa(j) + "]: ", fv.Index(j)})
			}
		case f.Type.Kind() == reflect.Pointer || f.Type.Kind() == reflect.Interface:
			kids = append(kids, child{f.Name + ": ", fv})
		case fv.IsZero() && f.Type.Kind() != reflect.Int && f.Type.Kind() != reflect.Uint64 && f.Type.Kind() != reflect.Float64:
			// false, "", and zero enums add noise; offsets and numbers stay
		case f.Type.Kind() == reflect.String:
			fmt.Fprintf(b, " %s=%q", f.Name, fv.String())
		default:
			fmt.Fprintf(b, " %s=%v", f.Name, fv.Interface())
		}
	}
	b.WriteByte('\n')
	for _, k := range kids {
		dump(b, k.label, k.v, depth+1)
	}
}
