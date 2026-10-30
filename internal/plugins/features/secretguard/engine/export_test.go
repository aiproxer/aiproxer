package engine

// DetectKnownPublicPrefixForTest exposes detectKnownPublicPrefix for white-box tests.
func DetectKnownPublicPrefixForTest(value string) string {
	return detectKnownPublicPrefix(value)
}
