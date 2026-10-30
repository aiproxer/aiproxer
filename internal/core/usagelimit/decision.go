package usagelimit

type AuthorityLevel string

const (
	AuthorityLevelAny           AuthorityLevel = ""
	AuthorityLevelEstimated     AuthorityLevel = "estimated"
	AuthorityLevelAuthoritative AuthorityLevel = "authoritative"
	AuthorityLevelUnavailable   AuthorityLevel = "unavailable"
)

func (a AuthorityLevel) IsKnown() bool {
	switch a {
	case AuthorityLevelAny, AuthorityLevelEstimated, AuthorityLevelAuthoritative, AuthorityLevelUnavailable:
		return true
	default:
		return false
	}
}

type DecisionOutcome string

const (
	DecisionOutcomeAllow       DecisionOutcome = "allow"
	DecisionOutcomeAdvisory    DecisionOutcome = "advisory"
	DecisionOutcomeDeny        DecisionOutcome = "deny"
	DecisionOutcomeClamp       DecisionOutcome = "clamp"
	DecisionOutcomeUnavailable DecisionOutcome = "unavailable"
	DecisionOutcomeError       DecisionOutcome = "error"
)
