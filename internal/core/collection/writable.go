package collection

// CheckWritable returns the error SaveRequest (k = KindRequest) or
// SaveFolder (k = KindFolder) would return for node id before encoding
// anything: CodeNotFound, CodeReadOnly or CodeWrongNode; nil when the node
// may be saved. Auto-save uses it to refuse an edit at once instead of
// when the delayed write runs.
func (t *Tree) CheckWritable(id string, k NodeKind) error {
	_, err := t.writableNode(id, k)
	return err
}
