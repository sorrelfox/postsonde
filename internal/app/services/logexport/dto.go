package logexport

// MaskInputDTO is what Mask takes.
type MaskInputDTO struct {
	// Text is the entry or log as the UI formatted it.
	Text string `json:"text"`
	// Format is "text" or "json" (a JSON document whose strings are
	// masked one by one, so it stays valid).
	Format string `json:"format"`
}

// MaskResultDTO is what Mask returns.
type MaskResultDTO struct {
	// Text is the input with every known secret masked as ••••••.
	Text string `json:"text"`
	// Masked is set when something was hidden.
	Masked bool `json:"masked"`
}

// SaveInputDTO is what Save takes.
type SaveInputDTO struct {
	// Title names the log in the save dialog ("Console", "Timeline").
	Title string `json:"title"`
	// FileName is the suggested file name; its extension is set from
	// Format. Blank is "postsonde-log".
	FileName string `json:"fileName"`
	// Format is "text" or "json".
	Format string `json:"format"`
	// Content is the whole log as the UI formatted it.
	Content string `json:"content"`
}

// SaveResultDTO is what Save returns.
type SaveResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Size is the bytes written.
	Size int64 `json:"size"`
	// Masked is set when a secret was hidden in the file.
	Masked bool `json:"masked"`
}
