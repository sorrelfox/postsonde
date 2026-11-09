package sse

import (
	"errors"
	"io"
	"iter"
)

// readBufferSize is how many bytes Events asks the reader for at a time.
const readBufferSize = 32 << 10

// Events parses r to its end and yields each event as soon as the read that
// completes it returns. A limit error or a failed read (CodeReadFailed) is
// yielded once with a zero Event, and iteration stops. At io.EOF the
// incomplete event, if any, is discarded.
func Events(r io.Reader, opts Options) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		p := NewParser(opts)
		defer p.Close()
		buf := make([]byte, readBufferSize)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				evs, perr := p.Feed(buf[:n])
				for _, ev := range evs {
					if !yield(ev, nil) {
						return
					}
				}
				if perr != nil {
					yield(Event{}, perr)
					return
				}
			}
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(Event{}, readFailed(err))
				return
			}
		}
	}
}
