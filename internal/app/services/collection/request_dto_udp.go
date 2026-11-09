package collection

import "github.com/sorrelfox/postsonde/internal/core/storage/projectfile"

// UDPOptionsDTO is a udp request's socket settings (UDP-01), as stored
// (docs/file-format.md#udp-request-kind-udp). Set for every udp request,
// left out for the other kinds.
type UDPOptionsDTO struct {
	// Connected receives only from the target; false from any sender.
	Connected bool `json:"connected"`
	// IPVersion is "" (either), "ipv4" or "ipv6".
	IPVersion string `json:"ipVersion"`
	// MaxDatagramBytes limits datagrams both ways; null is 65,535.
	MaxDatagramBytes *int64 `json:"maxDatagramBytes"`
	// OpenTimeoutMs bounds DNS and the proxy's association; null is 10 s,
	// 0 no limit.
	OpenTimeoutMs *int64 `json:"openTimeoutMs"`
}

// udpRequestDTO maps a udp request: Address, SendMode ("text", "hex" or
// "base64"), Message, UDP and Settings.Extra (the settings' unknown
// members).
func udpRequestDTO(r *projectfile.UDPRequest) RequestDTO {
	return RequestDTO{
		ID:           r.ID,
		Kind:         string(projectfile.KindUDP),
		Name:         r.Name,
		Description:  r.Description,
		Params:       make([]KeyValueDTO, 0),
		PathParams:   make([]KeyValueDTO, 0),
		Headers:      make([]KeyValueDTO, 0),
		Body:         BodyDTO{Fields: make([]KeyValueDTO, 0), Parts: make([]PartDTO, 0)},
		Address:      r.Address,
		SendMode:     r.SendMode,
		Message:      r.Message,
		Subprotocols: make([]string, 0),
		UDP: &UDPOptionsDTO{
			Connected:        r.Settings.Connected,
			IPVersion:        r.Settings.IPVersion,
			MaxDatagramBytes: clonePtr(r.Settings.MaxDatagramBytes),
			OpenTimeoutMs:    clonePtr(r.Settings.OpenTimeoutMs),
		},
		Settings: RequestSettingsDTO{Proxy: toProxyDTO(r.Settings.Proxy), Extra: extraText(r.Settings.Unknown)},
		Extra:    extraText(r.Unknown),
	}
}

// udpRequest builds a udp request file from in; a nil UDP is the
// defaults.
func (c *converter) udpRequest(in RequestDTO) *projectfile.UDPRequest {
	o := in.UDP
	if o == nil {
		o = &UDPOptionsDTO{}
	}
	return &projectfile.UDPRequest{
		Head:        projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: projectfile.KindUDP},
		ID:          in.ID,
		Name:        in.Name,
		Description: in.Description,
		Address:     in.Address,
		SendMode:    in.SendMode,
		Message:     in.Message,
		Settings: projectfile.UDPSettings{
			Connected:        o.Connected,
			IPVersion:        o.IPVersion,
			MaxDatagramBytes: clonePtr(o.MaxDatagramBytes),
			OpenTimeoutMs:    clonePtr(o.OpenTimeoutMs),
			Proxy:            c.proxy(in.Settings.Proxy),
			Unknown:          c.extra("settings.extra", in.Settings.Extra),
		},
		Unknown: c.extra("extra", in.Extra),
	}
}
