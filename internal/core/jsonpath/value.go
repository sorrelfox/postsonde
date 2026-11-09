package jsonpath

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
)

// MaxDecodeDepth is the deepest nesting of arrays and objects Decode
// accepts, the same as encoding/json's own limit.
const MaxDecodeDepth = 10000

// indexThreshold is the member count from which an Object keeps a name
// index; smaller objects are searched linearly.
const indexThreshold = 8

// Object is a JSON object that keeps its members in document order.
//
// Names are unique: when a document repeats a name, the member keeps the
// position of the first occurrence and the value of the last, like
// JavaScript's JSON.parse. The zero value is an empty object ready to use.
// An Object is not safe for concurrent writes; concurrent reads are fine.
type Object struct {
	names  []string
	values []any
	index  map[string]int
}

// NewObject returns an empty Object with room for n members.
func NewObject(n int) *Object {
	return &Object{names: make([]string, 0, n), values: make([]any, 0, n)}
}

// Len returns the number of members.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.names)
}

// Member returns the i-th member in document order.
func (o *Object) Member(i int) (name string, value any) {
	return o.names[i], o.values[i]
}

// Names returns the member names in document order.
func (o *Object) Names() []string {
	if o == nil {
		return nil
	}
	return slices.Clone(o.names)
}

// Get returns the value of the member called name.
func (o *Object) Get(name string) (any, bool) {
	if i := o.find(name); i >= 0 {
		return o.values[i], true
	}
	return nil, false
}

// Set adds a member at the end, or replaces the value of an existing
// member in place.
func (o *Object) Set(name string, value any) {
	if i := o.find(name); i >= 0 {
		o.values[i] = value
		return
	}
	o.names = append(o.names, name)
	o.values = append(o.values, value)
	if o.index != nil {
		o.index[name] = len(o.names) - 1
	} else if len(o.names) >= indexThreshold {
		o.index = make(map[string]int, len(o.names))
		for i, n := range o.names {
			o.index[n] = i
		}
	}
}

func (o *Object) find(name string) int {
	if o == nil {
		return -1
	}
	if o.index != nil {
		if i, ok := o.index[name]; ok {
			return i
		}
		return -1
	}
	for i, n := range o.names {
		if n == name {
			return i
		}
	}
	return -1
}

// MarshalJSON writes the members in document order. Number literals
// (json.Number) are written exactly as decoded.
func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i := range o.Len() {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(o.names[i])
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := json.Marshal(o.values[i])
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Decode parses one JSON text into the value model the evaluator works
// on best: objects are *Object (member order kept), arrays []any, numbers
// json.Number (the literal exactly as written), strings, bools and nil.
// Anything after the value other than whitespace is an error.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	type frame struct {
		obj  *Object
		arr  []any
		name string // pending member name while obj != nil
		key  bool   // the next string token is a member name
	}
	var stack []frame
	var result any
	done := false

	for !done {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		var v any
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if len(stack) >= MaxDecodeDepth {
					return nil, errors.New("jsonpath: JSON nested too deeply")
				}
				if t == '{' {
					stack = append(stack, frame{obj: NewObject(0), key: true})
				} else {
					stack = append(stack, frame{arr: []any{}})
				}
				continue
			case '}':
				v = stack[len(stack)-1].obj
				stack = stack[:len(stack)-1]
			case ']':
				v = stack[len(stack)-1].arr
				stack = stack[:len(stack)-1]
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].obj != nil && stack[n-1].key {
				stack[n-1].name = t
				stack[n-1].key = false
				continue
			}
			v = t
		default:
			v = t // nil, bool, json.Number
		}
		if len(stack) == 0 {
			result, done = v, true
			continue
		}
		top := &stack[len(stack)-1]
		if top.obj != nil {
			top.obj.Set(top.name, v)
			top.key = true
		} else {
			top.arr = append(top.arr, v)
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("jsonpath: unexpected data after the top-level JSON value")
		}
		return nil, err
	}
	return result, nil
}

// ---- generic access over the supported Go value types ----

// arrayOf returns v as a slice when it is a JSON array.
func arrayOf(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

// isObject reports whether v is a JSON object.
func isObject(v any) bool {
	switch o := v.(type) {
	case *Object:
		return o != nil
	case map[string]any:
		return true
	}
	return false
}

// objectLen returns the member count of an object value.
func objectLen(v any) int {
	switch o := v.(type) {
	case *Object:
		return o.Len()
	case map[string]any:
		return len(o)
	}
	return 0
}

// objectGet looks up a member of an object value.
func objectGet(v any, name string) (any, bool) {
	switch o := v.(type) {
	case *Object:
		return o.Get(name)
	case map[string]any:
		m, ok := o[name]
		return m, ok
	}
	return nil, false
}

// objectEach calls fn for each member in order (sorted names for a Go
// map) until fn returns false.
func objectEach(v any, fn func(name string, value any) bool) {
	switch o := v.(type) {
	case *Object:
		for i := range o.Len() {
			if !fn(o.names[i], o.values[i]) {
				return
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if !fn(k, o[k]) {
				return
			}
		}
	}
}

// kind classes used by comparisons.
const (
	kOther = iota
	kNull
	kBool
	kNumber
	kString
	kArray
	kObject
)

func kindOf(v any) int {
	switch x := v.(type) {
	case nil:
		return kNull
	case bool:
		return kBool
	case string:
		return kString
	case []any:
		return kArray
	case *Object:
		if x == nil {
			return kNull
		}
		return kObject
	case map[string]any:
		return kObject
	}
	if _, ok := numberOf(v); ok {
		return kNumber
	}
	return kOther
}

// numberOf returns v as an exact decimal when it is a JSON number.
func numberOf(v any) (decimal, bool) {
	switch n := v.(type) {
	case json.Number:
		return parseDecimal(string(n))
	case float64:
		return floatDecimal(n)
	case float32:
		return floatDecimal(float64(n))
	case int:
		return parseDecimal(strconv.FormatInt(int64(n), 10))
	case int8:
		return parseDecimal(strconv.FormatInt(int64(n), 10))
	case int16:
		return parseDecimal(strconv.FormatInt(int64(n), 10))
	case int32:
		return parseDecimal(strconv.FormatInt(int64(n), 10))
	case int64:
		return parseDecimal(strconv.FormatInt(n, 10))
	case uint:
		return parseDecimal(strconv.FormatUint(uint64(n), 10))
	case uint8:
		return parseDecimal(strconv.FormatUint(uint64(n), 10))
	case uint16:
		return parseDecimal(strconv.FormatUint(uint64(n), 10))
	case uint32:
		return parseDecimal(strconv.FormatUint(uint64(n), 10))
	case uint64:
		return parseDecimal(strconv.FormatUint(n, 10))
	}
	return decimal{}, false
}

func floatDecimal(f float64) (decimal, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return decimal{}, false
	}
	return parseDecimal(strconv.FormatFloat(f, 'g', -1, 64))
}
