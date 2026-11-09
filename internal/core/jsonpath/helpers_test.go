package jsonpath

import "encoding/json"

// dump formats a value as JSON for failure messages.
func dump(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
