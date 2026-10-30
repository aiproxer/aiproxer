package anthropic

import "testing"

func TestValidateCacheConfig(t *testing.T) {
	t.Parallel()
	if err := ValidateCacheConfig(CacheEnrollmentDisabled, ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCacheConfig("", ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCacheConfig(CacheEnrollmentAutomatic, "5m"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCacheConfig(CacheEnrollmentAutomatic, "1h"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{{"disabled", "5m"}, {"automatic", "10m"}, {"automatic", ""}, {"unknown", "5m"}} {
		if err := ValidateCacheConfig(CacheEnrollmentMode(tc[0]), tc[1]); err == nil {
			t.Fatalf("accepted mode=%q ttl=%q", tc[0], tc[1])
		}
	}
}
