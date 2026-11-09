package socketio

import (
	"bytes"

	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
)

// Decoder decodes the Socket.IO packets of one direction of a connection.
// Feed it every Engine.IO message in order: a BINARY_EVENT or BINARY_ACK is
// returned once its attachments, the binary messages right after it, have
// all arrived (spec "Packet encoding", binary packets). A Decoder is not
// safe for concurrent use.
type Decoder struct {
	opts    Options
	pending *Packet
	want    int
}

// NewDecoder returns a decoder with the given limits.
func NewDecoder(opts Options) *Decoder { return &Decoder{opts: opts} }

// Waiting returns how many attachments the decoder still expects.
func (d *Decoder) Waiting() int {
	if d.pending == nil {
		return 0
	}
	return d.want - len(d.pending.Attachments)
}

// Reset drops a partly received binary packet.
func (d *Decoder) Reset() { d.pending, d.want = nil, 0 }

// Feed decodes an Engine.IO message packet: text messages with DecodeText,
// binary ones with DecodeBinary.
func (d *Decoder) Feed(p engineio.Packet) (*Packet, error) {
	if p.Type != engineio.Message {
		return nil, protocolError(CodeNotMessage, "Only Engine.IO message packets carry Socket.IO packets; got "+p.Type.String()+".")
	}
	if p.Binary {
		return d.DecodeBinary(p.Data)
	}
	return d.DecodeText(string(p.Data))
}

// DecodeText decodes the text form of a packet. It returns nil and no
// error for a binary packet whose attachments are still to come. On an
// error the decoder is reset.
func (d *Decoder) DecodeText(s string) (*Packet, error) {
	if d.pending != nil {
		d.Reset()
		return nil, protocolError(CodeUnexpectedText, "A Socket.IO text packet arrived while binary attachments were expected.")
	}
	p, n, err := parseText(s, d.opts)
	if err != nil {
		return nil, err
	}
	if p.Type.IsBinary() {
		p.Attachments = make([][]byte, 0, n)
		if n > 0 {
			d.pending, d.want = &p, n
			return nil, nil
		}
	}
	return &p, nil
}

// DecodeBinary takes the next attachment. It returns the packet once its
// last attachment has arrived. The data is copied.
func (d *Decoder) DecodeBinary(b []byte) (*Packet, error) {
	if d.pending == nil {
		return nil, protocolError(CodeUnexpectedBinary, "A binary message arrived while no Socket.IO attachment was expected.")
	}
	d.pending.Attachments = append(d.pending.Attachments, bytes.Clone(b))
	if len(d.pending.Attachments) < d.want {
		return nil, nil
	}
	p := d.pending
	d.Reset()
	return p, nil
}

// Decode decodes one packet from its text form and attachments.
func Decode(text string, attachments [][]byte, opts Options) (Packet, error) {
	d := NewDecoder(opts)
	p, err := d.DecodeText(text)
	if err != nil {
		return Packet{}, err
	}
	for _, a := range attachments {
		if p != nil {
			return Packet{}, protocolError(CodeUnexpectedBinary, "The packet has more attachments than its text form declares.")
		}
		if p, err = d.DecodeBinary(a); err != nil {
			return Packet{}, err
		}
	}
	if p == nil {
		return Packet{}, protocolError(CodeInvalidAttachments, "The packet has fewer attachments than its text form declares.")
	}
	return *p, nil
}
