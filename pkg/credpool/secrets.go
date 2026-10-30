package credpool

// Secrets returns the ordered Secret field of each credential. Callers typically
// pass the result to inventory providers that only need a flat secret list.
// Returns nil when the input is empty.
func Secrets(credentials []Credential) []string {
	if len(credentials) == 0 {
		return nil
	}
	out := make([]string, 0, len(credentials))
	for _, cred := range credentials {
		out = append(out, cred.Secret)
	}
	return out
}
