package largebody

// ForceWholeMessageMarshalForTest switches w to the whole-message json.Marshal
// path that the streaming fixed-buffer implementation replaced. The switch is
// per instance so tests never share process-global state.
func ForceWholeMessageMarshalForTest(w *CallIdentityWriter) {
	w.wholeMessageMarshal = true
}
