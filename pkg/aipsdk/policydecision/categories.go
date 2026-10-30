package policydecision

// Client category values carried on Record.ClientCategory. They are the
// evidence taxonomy shared by core extension runners and projected records
// (requirements 1.7, 4.6, 9.1). Only ClientCategory and ClientMessage are
// intended for frontend use; the values mirror the stable policy error kinds
// so a projected record can be classified the same way as an explicit policy
// error. These constants are the canonical owner of the category strings;
// extensions aliases and policy error helpers reference them where boundary
// rules permit. Wire/JSON values are unchanged.
const (
	CategoryAllowed   = "policy_allowed"
	CategorySkipped   = "policy_skipped"
	CategoryDenied    = "policy_denied"
	CategoryFailure   = "policy_failure"
	CategoryObserved  = "policy_observed"
	CategoryMalformed = "policy_malformed"
)
