package stdhttp

// Test-only accessors for unexported cancel-header constants.
// Used by mount_constants_test.go to verify alignment with sessionwire.

func ExportHeaderAuthoritativeSessionID() string { return headerAuthoritativeSessionID }
func ExportHeaderResumeToken() string            { return headerResumeToken }
func ExportGeminiFrontendID() string             { return geminiFrontendID }
