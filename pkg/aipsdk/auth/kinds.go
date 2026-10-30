package auth

// HandlerKind selects the configured auth handler.
type HandlerKind string

// RequiredLevel is the policy-required authentication level for a route or deployment.
type RequiredLevel string

// DecisionOutcome is the result of an auth decision.
type DecisionOutcome string

// ChallengeKind categorizes a non-terminal auth challenge (no secret material).
type ChallengeKind string

const (
	HandlerLocalNoop   HandlerKind = "local_noop"
	HandlerLocalAPIKey HandlerKind = "local_api_key"
	HandlerRemote      HandlerKind = "remote"

	LevelNone      RequiredLevel = "none"
	LevelAPIKey    RequiredLevel = "api_key"
	LevelAPIKeySSO RequiredLevel = "api_key_sso"

	OutcomeAllow     DecisionOutcome = "allow"
	OutcomeDeny      DecisionOutcome = "deny"
	OutcomeChallenge DecisionOutcome = "challenge"

	// ChallengeSSORequired indicates a recognized app/device key but an unmet or missing SSO step.
	ChallengeSSORequired ChallengeKind = "sso_required"
)
