package engine

// AccessMode defines feature-local execution postures for secret sourcing.
// It is closed and internal to the feature engine; composition translates
// host access mode to this type.
type AccessMode uint8

const (
	ModeSingleUser AccessMode = iota
	ModeMultiUser
)

func (m AccessMode) String() string {
	switch m {
	case ModeMultiUser:
		return "multi_user"
	default:
		return "single_user"
	}
}
