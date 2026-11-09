package jsonpath

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// Declared types of function parameters and results (RFC 9535 §2.4.1).
type ftype int

const (
	valueType ftype = iota
	logicalType
	nodesType
)

func (t ftype) String() string {
	switch t {
	case logicalType:
		return "LogicalType"
	case nodesType:
		return "NodesType"
	}
	return "ValueType"
}

// function is a function extension.
type function struct {
	name   string
	params []ftype
	result ftype
}

// functions are the function extensions of RFC 9535 §2.4.4–2.4.8.
var functions = map[string]*function{
	"length": {name: "length", params: []ftype{valueType}, result: valueType},
	"count":  {name: "count", params: []ftype{nodesType}, result: valueType},
	"match":  {name: "match", params: []ftype{valueType, valueType}, result: logicalType},
	"search": {name: "search", params: []ftype{valueType, valueType}, result: logicalType},
	"value":  {name: "value", params: []ftype{nodesType}, result: valueType},
}

// nothing is the special result "Nothing": no value (RFC 9535 §2.4.1).
type nothingT struct{}

var nothing any = nothingT{}

// callValue evaluates a ValueType function; the result may be nothing.
func (e *evaluator) callValue(f *funcExpr, cur any) any {
	switch f.fn.name {
	case "length":
		v := e.value(f.args[0], cur)
		switch x := v.(type) {
		case string:
			e.charge(len(x) / 64)
			return json.Number(strconv.Itoa(utf8.RuneCountInString(x)))
		case []any:
			return json.Number(strconv.Itoa(len(x)))
		}
		if isObject(v) {
			return json.Number(strconv.Itoa(objectLen(v)))
		}
		return nothing
	case "count":
		return json.Number(strconv.Itoa(len(e.nodes(f.args[0], cur))))
	case "value":
		ns := e.nodes(f.args[0], cur)
		if len(ns) == 1 {
			return ns[0].v
		}
		return nothing
	}
	return nothing
}

// callLogical evaluates match() or search().
func (e *evaluator) callLogical(f *funcExpr, cur any) bool {
	s, ok := e.value(f.args[0], cur).(string)
	if !ok {
		return false
	}
	full := f.fn.name == "match"
	if f.reSet {
		if f.reErr {
			return false
		}
		e.charge(len(s) / 16)
		return f.re.MatchString(s)
	}
	pat, ok := e.value(f.args[1], cur).(string)
	if !ok {
		return false
	}
	re := e.regexp(pat, full)
	if re == nil {
		return false
	}
	e.charge(len(s) / 16)
	return re.MatchString(s)
}
