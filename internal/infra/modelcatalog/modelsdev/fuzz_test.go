package modelsdev

import (
	"testing"
	"time"
)

func FuzzParseSnapshot(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseSnapshot(b, time.Unix(0, 0).UTC())
	})
}
