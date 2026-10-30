package reasoninge2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func CheckResponsesHistoryIDs(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("reasoninge2e responses: structural mismatch: reasoning_count got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if idToken(got[i]) != idToken(want[i]) {
			return fmt.Errorf("reasoninge2e responses: structural mismatch: reasoning_id index=%d detail=token_mismatch", i)
		}
	}
	return nil
}

func idToken(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}
