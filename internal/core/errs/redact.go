package errs

import "errors"

// Redact returns err with every text it carries passed through redact:
// the message, hint and detail values of each *Error in its chain, and the
// messages of the other errors in it. It is for errors that may quote a
// secret they did not choose to, such as a library error repeating a URL
// whose query holds a secret variable's value ("Get \"https://…?key=…\":
// EOF"); redact is typically a secret masker's MaskText.
//
// When redact changes nothing, err itself is returned. Otherwise the result
// keeps the kinds, codes and structure (errors.Is, KindOf and Chain work
// as before; errors.Is matches what the original matched), but a cause
// that is not an *Error becomes a plain error with the redacted text, so
// errors.As no longer finds its original type.
func Redact(err error, redact func(string) string) error {
	if err == nil || redact == nil || !needsRedact(err, redact, 0) {
		return err
	}
	return redactErr(err, redact, 0)
}

func needsRedact(err error, fn func(string) string, depth int) bool {
	if err == nil {
		return false
	}
	if depth > maxChainDepth {
		return fn(err.Error()) != err.Error()
	}
	switch u := err.(type) {
	case *Error:
		if fn(u.Message) != u.Message || fn(u.Hint) != u.Hint {
			return true
		}
		for _, v := range u.Details {
			if fn(v) != v {
				return true
			}
		}
		return needsRedact(u.Err, fn, depth+1)
	case interface{ Unwrap() []error }:
		return fn(err.Error()) != err.Error()
	case interface{ Unwrap() error }:
		child := u.Unwrap()
		own := ownMessage(err, child)
		return fn(own) != own || needsRedact(child, fn, depth+1)
	}
	return fn(err.Error()) != err.Error()
}

func redactErr(err error, fn func(string) string, depth int) error {
	if err == nil {
		return nil
	}
	if depth > maxChainDepth {
		return &redacted{msg: fn(err.Error()), orig: err}
	}
	switch u := err.(type) {
	case *Error:
		c := u.clone()
		c.Message = fn(u.Message)
		c.Hint = fn(u.Hint)
		for k, v := range c.Details {
			c.Details[k] = fn(v)
		}
		c.Err = redactErr(u.Err, fn, depth+1)
		return c
	case interface{ Unwrap() []error }:
		// A join's children are flattened into its text.
		return &redacted{msg: fn(err.Error()), orig: err}
	case interface{ Unwrap() error }:
		child := u.Unwrap()
		return &redacted{msg: fn(ownMessage(err, child)), cause: redactErr(child, fn, depth+1), orig: err}
	}
	return &redacted{msg: fn(err.Error()), orig: err}
}

// redacted stands in for an error that is not an *Error: its own message,
// redacted, and its redacted cause.
type redacted struct {
	msg   string
	cause error
	// orig is the error it stands in for, for errors.Is only: its text is
	// never used.
	orig error
}

func (r *redacted) Error() string {
	switch {
	case r.cause == nil:
		return r.msg
	case r.msg == "":
		return r.cause.Error()
	}
	return r.msg + ": " + r.cause.Error()
}

func (r *redacted) Unwrap() error { return r.cause }

func (r *redacted) Is(target error) bool { return errors.Is(r.orig, target) }
