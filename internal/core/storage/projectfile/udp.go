package projectfile

import "github.com/sorrelfox/postsonde/internal/core/storage/stablejson"

// KindUDP is a UDP request file (UDP-01).
const KindUDP Kind = "udp"

// Send modes of a UDP request, for UDPRequest.SendMode: SendText (the
// default), SendHex and SendBase64.
const SendBase64 = "base64"

// IP versions of a UDP request, for UDPSettings.IPVersion: "" for either
// (as the name resolves), IPv4 or IPv6 only.
const (
	IPVersion4 = "ipv4"
	IPVersion6 = "ipv6"
)

// UDPRequest is a UDP request file (kind "udp", UDP-01): where datagrams
// go and the datagram being composed. Datagrams go through the proxy in
// effect like every connection: a SOCKS5 proxy relays them with UDP
// ASSOCIATE (SOCKS-07).
type UDPRequest struct {
	Head
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Address is "host:port" as typed, with {{variables}} unresolved.
	Address string `json:"address"`
	// SendMode is how Message is turned into bytes: "text" (UTF-8 with
	// {{variables}} rendered, the default, written as a missing member),
	// "hex" or "base64". A mode this version does not know is kept.
	SendMode string `json:"sendMode,omitempty"`
	// Message is the composer's draft as typed.
	Message  string      `json:"message,omitempty"`
	Settings UDPSettings `json:"settings,omitzero"`
	Unknown  Unknown     `json:"-"`
}

// UDPSettings are a UDP request's socket options. Unset members are the
// defaults; when all are unset the member is left out of the file.
type UDPSettings struct {
	// Connected receives only from the target and reports ICMP "port
	// unreachable"; unset (false) receives from any sender.
	Connected bool `json:"connected,omitempty"`
	// IPVersion is "" (either), "ipv4" or "ipv6". A value this version
	// does not know is kept, and refused when sending.
	IPVersion string `json:"ipVersion,omitempty"`
	// MaxDatagramBytes limits datagrams: larger ones are not sent, and
	// longer received ones are cut. Missing means 65,535 (no limit beyond
	// UDP's own).
	MaxDatagramBytes *int64 `json:"maxDatagramBytes,omitempty"`
	// OpenTimeoutMs bounds DNS and the proxy's UDP association, in
	// milliseconds; missing means 10 s, 0 no limit.
	OpenTimeoutMs *int64  `json:"openTimeoutMs,omitempty"`
	Unknown       Unknown `json:"-"`
}

func (*UDPRequest) FileKind() Kind { return KindUDP }
func (r *UDPRequest) head() Head   { return r.Head }

func (r UDPRequest) MarshalJSON() ([]byte, error) {
	type plain UDPRequest
	// The default is written as a missing member.
	if r.SendMode == SendText {
		r.SendMode = ""
	}
	return stablejson.MarshalWithExtra(plain(r), r.Unknown)
}

func (r *UDPRequest) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain UDPRequest
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*r = UDPRequest(p)
	return nil
}

func (s UDPSettings) MarshalJSON() ([]byte, error) {
	type plain UDPSettings
	return stablejson.MarshalWithExtra(plain(s), s.Unknown)
}

func (s *UDPSettings) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain UDPSettings
	var p plain
	u, err := decodeObject(data, &p)
	if err != nil {
		return err
	}
	p.Unknown = u
	*s = UDPSettings(p)
	return nil
}
