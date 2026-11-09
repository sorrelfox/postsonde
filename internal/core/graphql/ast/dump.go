package ast

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var (
	locType  = reflect.TypeFor[Loc]()
	nodeType = reflect.TypeFor[Node]()
)

// Dump returns an indented, deterministic text form of the tree rooted at n,
// one node per line with its byte range and scalar fields, for tests and
// debugging:
//
//	OperationDefinition [0:9] Operation=query Keyword=-1
//	  SelectionSet: SelectionSet [0:9] LBrace=0 RBrace=8
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
		case !f.IsExported():
		case f.Type == locType:
			fmt.Fprintf(b, " [%d:%d]", fv.Field(0).Int(), fv.Field(1).Int())
		case f.Name == "Source":
		case f.Type.Kind() == reflect.Slice:
			for j := range fv.Len() {
				kids = append(kids, child{f.Name + "[" + strconv.Itoa(j) + "]: ", fv.Index(j)})
			}
		case f.Type.Implements(nodeType) || f.Type.Kind() == reflect.Interface:
			kids = append(kids, child{f.Name + ": ", fv})
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
