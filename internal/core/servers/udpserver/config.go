package udpserver

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
)

// Formats of Config.ReplyFormat.
const (
	FormatText = "text"
	FormatHex  = "hex"
)

// MaxDatagram is the largest UDP payload over IPv4 (65,535 minus the IP
// and UDP headers): the largest reply and send.
const MaxDatagram = 65507

// CodeInvalidConfig: a configuration the UDP server cannot use.
const CodeInvalidConfig = "udpserver.invalid_config"

// Config is the configuration of a UDP server (Spec.Config).
type Config struct {
	// Echo sends every datagram back to its sender.
	Echo bool
	// FixedReply sends Reply to the sender of every datagram (after the
	// echo when both are on).
	FixedReply bool
	// Reply is the fixed reply: text sent as UTF-8, or hex bytes
	// (ReplyFormat).
	Reply string
	// ReplyFormat is FormatText (the default for "") or FormatHex.
	ReplyFormat string
}

// DefaultConfig is the configuration of a new UDP server: echo on.
func DefaultConfig() Config {
	return Config{Echo: true, ReplyFormat: FormatText}
}

// Normalize checks c and returns it with the format defaulted.
func Normalize(c Config) (Config, error) {
	if c.ReplyFormat == "" {
		c.ReplyFormat = FormatText
	}
	if _, err := c.replyBytes(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// replyBytes returns the bytes of the fixed reply.
func (c Config) replyBytes() ([]byte, error) {
	var b []byte
	switch c.ReplyFormat {
	case FormatText, "":
		if !utf8.ValidString(c.Reply) {
			return nil, invalidConfig("reply", "The reply must be valid UTF-8 text.")
		}
		b = []byte(c.Reply)
	case FormatHex:
		var err error
		if b, err = hexbytes.Parse(c.Reply); err != nil {
			return nil, errs.Wrap(err, errs.InvalidArgument, "The reply is not valid hex: "+messageOf(err)).
				WithCode(CodeInvalidConfig).WithDetail("field", "reply")
		}
	default:
		return nil, invalidConfig("replyFormat", fmt.Sprintf("%q is not a reply format; use text or hex.", c.ReplyFormat))
	}
	if len(b) > MaxDatagram {
		return nil, invalidConfig("reply", fmt.Sprintf("The reply is %d bytes; a UDP datagram holds at most %d.", len(b), MaxDatagram))
	}
	return b, nil
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

func messageOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
