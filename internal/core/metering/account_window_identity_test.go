package metering

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// legacyAccountWindowEncoding retains the previous fmt implementation as an
// independent compatibility reference for grouping and pagination identities.
func legacyAccountWindowEncoding(values ...string) string {
	var builder strings.Builder
	for _, value := range values {
		fmt.Fprintf(&builder, "%d:", len(value))
		builder.WriteString(value)
	}
	return builder.String()
}

func legacyAccountWindowIdentity(subject aipsdkmetering.SubjectRef, tenant string) string {
	return legacyAccountWindowEncoding(
		subject.StoreID, tenant, subject.ProviderAccountKey, subject.PoolID,
		subject.WindowID, fmt.Sprintf("%d", subject.ResetAt.UTC().UnixNano()),
	)
}

func TestAccountWindowLengthPrefixed_EncodingCompatibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{name: "no-fields", want: ""},
		{name: "empty-fields", values: []string{"", ""}, want: "0:0:"},
		{name: "byte-lengths-and-delimiters", values: []string{"a:b", "", "é", "\x00\xff"}, want: "3:a:b0:2:é2:\x00\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountWindowLengthPrefixed(tc.values...); got != tc.want {
				t.Fatalf("encoding = %q, want %q", got, tc.want)
			}
		})
	}
	for _, length := range []int{9, 10, 99, 100, 999, 1000} {
		values := []string{strings.Repeat("x", length), "🙂", ""}
		if got, want := accountWindowLengthPrefixed(values...), legacyAccountWindowEncoding(values...); got != want {
			t.Fatalf("length %d encoding differs", length)
		}
	}
}

func TestAccountWindowIdentity_EncodingCompatibility(t *testing.T) {
	t.Parallel()
	for _, reset := range []time.Time{
		{},
		time.Unix(0, 0), time.Unix(0, -1),
		time.Unix(0, math.MinInt64), time.Unix(0, math.MaxInt64),
		time.Unix(1700000000, 123456789).In(time.FixedZone("offset", 19800)),
	} {
		subject := aipsdkmetering.SubjectRef{
			StoreID: "store:1", TenantID: "  tenant  ", ProviderAccountKey: "租户🙂",
			PoolID: "", WindowID: "window", ResetAt: reset,
		}
		want := legacyAccountWindowIdentity(subject, "tenant")
		if got := (AccountWindowProjection{Subject: subject}).IdentityKey(); got != want {
			t.Fatalf("reset %v identity differs", reset)
		}
		observation := aipsdkmetering.Observation{
			Subject: subject, Correlation: aipsdkmetering.CorrelationV2{TenantID: "  fallback  "},
		}
		observation.Subject.TenantID = " "
		if got, want := accountWindowIdentity(observation), legacyAccountWindowIdentity(observation.Subject, "fallback"); got != want {
			t.Fatalf("reset %v fallback identity differs", reset)
		}
	}

	random := rand.New(rand.NewSource(722))
	for i := range 1000 {
		values := make([]string, 6)
		for j := range values {
			data := make([]byte, random.Intn(300))
			if _, err := random.Read(data); err != nil {
				t.Fatal(err)
			}
			values[j] = string(data)
		}
		if got, want := accountWindowLengthPrefixed(values...), legacyAccountWindowEncoding(values...); got != want {
			t.Fatalf("random vector %d length encoding differs", i)
		}
		subject := aipsdkmetering.SubjectRef{
			StoreID: values[0], ProviderAccountKey: values[2], PoolID: values[3],
			WindowID: values[4], ResetAt: time.Unix(0, int64(random.Uint64())),
		}
		if got, want := accountWindowIdentityForSubjectWithTenant(subject, values[1]), legacyAccountWindowIdentity(subject, values[1]); got != want {
			t.Fatalf("random vector %d identity differs", i)
		}
	}
}

var accountWindowIdentityBenchmarkSink string

// BenchmarkAccountWindowIdentityEncoding measures only identifier generation,
// not application throughput or end-to-end projection performance.
func BenchmarkAccountWindowIdentityEncoding(b *testing.B) {
	subject := aipsdkmetering.SubjectRef{
		StoreID: "store-1", ProviderAccountKey: "provider-account-123",
		PoolID: "pool-a", WindowID: "window-a", ResetAt: time.Unix(1700000000, 123456789),
	}
	for _, variant := range []struct {
		name   string
		encode func(aipsdkmetering.SubjectRef, string) string
	}{
		{name: "LegacyFmt", encode: legacyAccountWindowIdentity},
		{name: "Current", encode: accountWindowIdentityForSubjectWithTenant},
	} {
		b.Run(variant.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				accountWindowIdentityBenchmarkSink = variant.encode(subject, "tenant-1")
			}
		})
	}
}
