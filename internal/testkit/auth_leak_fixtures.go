package testkit

// AuthLeakFixtureSecrets returns synthetic strings that must not appear in operator-visible
// outputs in auth regression tests (sinks, HTTP bodies). Kept in test support code only.
func AuthLeakFixtureSecrets() []string {
	return []string{
		"raw-api-key-FIXTURE_aaaaaaaa",
		"Bearer FIXTURE_bbbbbbbb",
		"sso-fixture-token-cccccccc",
		"resume-proof-fixture-dddddddd",
		"oauth-user-access-fixture-eeeeeeee",
	}
}
