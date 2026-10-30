// Package secretguard defines opaque SDK contracts for the secrets-guard ingress stage.
//
// Consumers receive Matcher / MatcherResolver capabilities that scan and redact
// without exposing raw catalog values. Decision.Validate rejects malformed
// decision shapes before the runner emits evidence or continues downstream. No
// type in this package returns secret bytes or accepts an environment reader at
// request time (design rules D3–D4).
package secretguard
