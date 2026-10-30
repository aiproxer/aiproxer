package usagelimit

type RuleKind string

const (
	RuleKindQuota RuleKind = "quota"
	RuleKindRate  RuleKind = "rate"
)

func (k RuleKind) IsKnown() bool {
	switch k {
	case RuleKindQuota, RuleKindRate:
		return true
	default:
		return false
	}
}
