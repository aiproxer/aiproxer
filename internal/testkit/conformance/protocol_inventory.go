package conformance

// Protocol identities support independent parity discovery. They are not paired
// into a mandatory frontend-by-backend completeness list.
func BundledFrontendIDs() []string {
	return []string{"openai-responses", "openai-legacy", "anthropic", "gemini", "openresponses"}
}

func BundledBackendIDs() []string {
	return []string{"openai-responses", "openai-legacy", "anthropic", "gemini", "bedrock", "acp", "openresponses", "openrouter", "nvidia"}
}
