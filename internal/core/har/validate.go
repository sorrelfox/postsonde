package har

import (
	"encoding/json"
	"strconv"
)

// Validate checks a HAR document against the members HAR 1.2 requires
// (http://www.softwareishard.com/blog/har-12-spec/): their presence and
// JSON types, and that send, wait and receive are not negative. It
// returns the problems found, each as "path: problem"; none for a valid
// file. Optional members are not checked.
func Validate(data []byte) []string {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return []string{"the file is not JSON: " + err.Error()}
	}
	v := &validator{}
	log := v.object(root, "", "log")
	if log == nil {
		return v.problems
	}
	v.str(log, "log", "version")
	if c := v.object(log, "log", "creator"); c != nil {
		v.str(c, "log.creator", "name")
		v.str(c, "log.creator", "version")
	}
	if pages, ok := log["pages"]; ok {
		list, ok := pages.([]any)
		if !ok {
			v.add("log.pages", "not a list")
		}
		for i, p := range list {
			at := "log.pages[" + strconv.Itoa(i) + "]"
			pm, ok := p.(map[string]any)
			if !ok {
				v.add(at, "not an object")
				continue
			}
			v.str(pm, at, "startedDateTime")
			v.str(pm, at, "id")
			v.str(pm, at, "title")
			v.object(pm, at, "pageTimings")
		}
	}
	for i, e := range v.list(log, "log", "entries") {
		at := "log.entries[" + strconv.Itoa(i) + "]"
		em, ok := e.(map[string]any)
		if !ok {
			v.add(at, "not an object")
			continue
		}
		v.str(em, at, "startedDateTime")
		v.num(em, at, "time")
		v.object(em, at, "cache")
		if r := v.object(em, at, "request"); r != nil {
			at := at + ".request"
			v.str(r, at, "method")
			v.str(r, at, "url")
			v.str(r, at, "httpVersion")
			v.pairs(r, at, "cookies")
			v.pairs(r, at, "headers")
			v.pairs(r, at, "queryString")
			v.num(r, at, "headersSize")
			v.num(r, at, "bodySize")
			if pd, ok := r["postData"]; ok {
				pm, ok := pd.(map[string]any)
				if !ok {
					v.add(at+".postData", "not an object")
				} else {
					v.str(pm, at+".postData", "mimeType")
				}
			}
		}
		if r := v.object(em, at, "response"); r != nil {
			at := at + ".response"
			v.num(r, at, "status")
			v.str(r, at, "statusText")
			v.str(r, at, "httpVersion")
			v.pairs(r, at, "cookies")
			v.pairs(r, at, "headers")
			if c := v.object(r, at, "content"); c != nil {
				v.num(c, at+".content", "size")
				v.str(c, at+".content", "mimeType")
			}
			v.str(r, at, "redirectURL")
			v.num(r, at, "headersSize")
			v.num(r, at, "bodySize")
		}
		if t := v.object(em, at, "timings"); t != nil {
			for _, k := range []string{"send", "wait", "receive"} {
				if n, ok := v.num(t, at+".timings", k); ok && n < 0 {
					v.add(at+".timings."+k, "is negative")
				}
			}
		}
	}
	return v.problems
}

type validator struct{ problems []string }

func (v *validator) add(at, msg string) { v.problems = append(v.problems, at+": "+msg) }

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func (v *validator) object(parent any, at, key string) map[string]any {
	pm, _ := parent.(map[string]any)
	if pm == nil {
		v.add(join(at, key), "missing")
		return nil
	}
	x, ok := pm[key]
	if !ok {
		v.add(join(at, key), "missing")
		return nil
	}
	m, ok := x.(map[string]any)
	if !ok {
		v.add(join(at, key), "not an object")
	}
	return m
}

func (v *validator) list(m map[string]any, at, key string) []any {
	x, ok := m[key]
	if !ok {
		v.add(join(at, key), "missing")
		return nil
	}
	l, ok := x.([]any)
	if !ok {
		v.add(join(at, key), "not a list")
	}
	return l
}

func (v *validator) str(m map[string]any, at, key string) {
	x, ok := m[key]
	if !ok {
		v.add(join(at, key), "missing")
		return
	}
	if _, ok := x.(string); !ok {
		v.add(join(at, key), "not a string")
	}
}

func (v *validator) num(m map[string]any, at, key string) (float64, bool) {
	x, ok := m[key]
	if !ok {
		v.add(join(at, key), "missing")
		return 0, false
	}
	n, ok := x.(float64)
	if !ok {
		v.add(join(at, key), "not a number")
	}
	return n, ok
}

// pairs checks a list of objects with a string name and value.
func (v *validator) pairs(m map[string]any, at, key string) {
	for i, x := range v.list(m, at, key) {
		p := join(at, key) + "[" + strconv.Itoa(i) + "]"
		o, ok := x.(map[string]any)
		if !ok {
			v.add(p, "not an object")
			continue
		}
		v.str(o, p, "name")
		v.str(o, p, "value")
	}
}
