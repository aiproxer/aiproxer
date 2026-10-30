package anthropicmessages

import (
	"testing"
)

func TestCacheEnrollmentOptionsDisabledByDefault(t *testing.T) {
	t.Parallel()
	if got := cacheEnrollmentOptions(Config{}); got != nil {
		t.Fatalf("options=%v", got)
	}
}

func TestValidateCacheEnrollment(t *testing.T) {
	t.Parallel()
	for _, tc := range [][3]string{{"", "", "ok"}, {"disabled", "", "ok"}, {"automatic", "5m", "ok"}, {"automatic", "1h", "ok"}, {"automatic", "10m", "bad"}, {"disabled", "5m", "bad"}} {
		err := validateCacheEnrollment(tc[0], tc[1])
		if (err == nil) != (tc[2] == "ok") {
			t.Fatalf("mode=%q ttl=%q err=%v", tc[0], tc[1], err)
		}
	}
}
