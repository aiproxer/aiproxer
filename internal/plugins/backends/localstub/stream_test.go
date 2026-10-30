package localstub

import (
	"testing"
)

func BenchmarkEventStreamForConfig(b *testing.B) {
	cfg := Config{
		Text:                      "hello world",
		StreamErrorAfterTextDelta: true,
	}

	for b.Loop() {
		_ = eventStreamForConfig(cfg)
	}
}
