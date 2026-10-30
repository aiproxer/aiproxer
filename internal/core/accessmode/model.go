package accessmode

import (
	"fmt"
	"strings"
)

// Mode is deployment access posture (immutable after startup).
type Mode string

const (
	ModeSingleUser Mode = "single_user"
	ModeMultiUser  Mode = "multi_user"
)

// NormalizeMode maps YAML access.mode to a typed mode. Empty string defaults to single_user.
func NormalizeMode(raw string) (Mode, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ModeSingleUser, nil
	}
	switch s {
	case string(ModeSingleUser):
		return ModeSingleUser, nil
	case string(ModeMultiUser):
		return ModeMultiUser, nil
	default:
		return "", fmt.Errorf("%w: %q (want single_user or multi_user)", ErrUnknownAccessMode, raw)
	}
}
