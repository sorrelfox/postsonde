package socketio

import (
	"encoding/json"
	"errors"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
)

// parseArgs turns the args text into emit arguments (core.ParseArgs),
// with the service's error code.
func parseArgs(text string) ([]any, error) {
	args, err := core.ParseArgs(text)
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Code == core.CodeInvalidArgs {
		c := *e
		c.Code = CodeInvalidArgs
		return nil, &c
	}
	return args, err
}

// displayArgs is the JSON text of arguments for the log (core.DisplayArgs).
func displayArgs(data json.RawMessage, atts [][]byte, skip int) string {
	return core.DisplayArgs(data, atts, skip)
}
