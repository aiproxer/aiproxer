package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func truncErrDetail(err error, max int) string {
	if err == nil || max <= 0 {
		return ""
	}
	r := []rune(err.Error())
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}

func stableCallToken(call *aipapi.Call) string {
	if call == nil {
		return hex.EncodeToString(make([]byte, 8))
	}
	cp := *call
	cp.ID = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return hex.EncodeToString(make([]byte, 8))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func capBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
