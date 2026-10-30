package keepwarm

import (
	cryptorand "crypto/rand"
	"encoding/hex"
)

func newManagerNamespace() (string, error) {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
