package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2ReviewMediaRequiresDirection(t *testing.T) {
	for _, component := range []string{metering.ComponentImage, metering.ComponentAudio, metering.ComponentVideo, metering.ComponentFile} {
		key := metering.ComponentKey{Direction: metering.DirectionNone, Component: component, Unit: metering.UnitByte, SchemaID: "review-media-v2"}
		if err := key.Validate(); err == nil {
			t.Errorf("ambiguous %s direction accepted", component)
		}
	}
}

func TestPhase2ReviewIdentityRejectsInvalidUTF8(t *testing.T) {
	for _, invalid := range []string{string([]byte{0xff}), string([]byte{0xfe})} {
		key := metering.ComponentKey{Direction: metering.DirectionInput, Component: "custom:" + invalid, Unit: metering.UnitByte, SchemaID: "review-v2"}
		if err := key.Validate(); err == nil {
			t.Errorf("invalid UTF-8 identity accepted; JSON hashing rewrites its bytes: %q", key.CanonicalKey())
		}
	}
}
