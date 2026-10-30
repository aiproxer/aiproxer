package reasoningpreservation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCompressionClaim(t *testing.T) {
	t.Parallel()

	digest := [32]byte{1}
	entry := &compressionEntry{claim: compressionClaim{
		originalDigest: digest,
		policyRevision: "policy-v1",
	}}
	tests := []struct {
		name      string
		claim     CompressionClaim
		wantError string
	}{
		{
			name: "correct claim",
			claim: CompressionClaim{
				OriginalDigest: digest,
				PolicyRevision: "policy-v1",
			},
		},
		{
			name: "digest mismatch",
			claim: CompressionClaim{
				OriginalDigest: [32]byte{2},
				PolicyRevision: "policy-v1",
			},
			wantError: ErrCompressionConflict.Error() + ": digest mismatch",
		},
		{
			name: "policy mismatch",
			claim: CompressionClaim{
				OriginalDigest: digest,
				PolicyRevision: "policy-v2",
			},
			wantError: ErrCompressionConflict.Error() + ": policy mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateCompressionClaim(entry, tc.claim)
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrCompressionConflict)
			require.EqualError(t, err, tc.wantError)
		})
	}
}
