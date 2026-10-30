package b2bua

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomALegID returns an opaque A-leg identifier (prefix "a_" + 32 hex chars).
func RandomALegID() (string, error) {
	return randomLegID('a')
}

// RandomBLegID returns an opaque B-leg identifier (prefix "b_" + 32 hex chars).
func RandomBLegID() (string, error) {
	return randomLegID('b')
}

func randomLegID(prefix rune) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("b2bua: leg id: %w", err)
	}
	return fmt.Sprintf("%c_%s", prefix, hex.EncodeToString(b[:])), nil
}
