package demo

// ItemDTO is one item of the demo stream.
type ItemDTO struct {
	// Index counts items from 0, so the UI can check that none is missing.
	Index int64 `json:"index"`
	// Text is a small payload, "item <index>".
	Text string `json:"text"`
}
