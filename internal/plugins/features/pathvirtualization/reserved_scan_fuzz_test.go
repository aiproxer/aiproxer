package pathvirtualization_test

import (
	"bytes"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// Spec: b-leg-path-virtualization Task 8.1. design.md "Testing Strategy" asks for a
// fuzz parser that proves panic freedom and a bounded answer over arbitrary untrusted
// bytes. The recognizer runs over a WHOLE completed argument payload, so its input is
// arbitrary bytes rather than a path, which makes total behavior and panic freedom the
// two properties worth pinning.
//
// The corpus is deliberately hostile: truncated JSON, nested containers that never
// close, raw invalid UTF-8, the marker repeated many times, and marker bytes at the
// very end of the input.
func FuzzScanReservedAliasIsTotalAndBounded(f *testing.F) {
	for _, seed := range []string{
		"",
		"/.__aip_v1__/w_" + scanTag + "/a.go",
		`{"path":"/.__aip_v1__/w_` + scanTag + `/a.go"}`,
		`{"path":"/.__aip_v1__/w_short`,
		"/.__aip_v1__",
		"/.__aip_v1__/",
		`.__aip_v1__\w_` + scanTag + `\a.go`,
		`{"content":"/.__aip_v1__/w_` + scanTag + `/","x":[[[[[`,
		`/` + `.__aip_v1__/w_short/` + `x`,
		"\xff\xfe.__aip_v1__\xff/w_short/x",
		`{"path":"` + scanTag + `"}`,
		`\\?\C:\.__aip_v1__\w_` + scanTag + `\a.go`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		got := pathvirtualization.ScanReservedAlias(raw)
		switch got {
		case pathvirtualization.ReservedAliasAbsent,
			pathvirtualization.ReservedAliasMalformedTag,
			pathvirtualization.ReservedAliasWellFormed:
		default:
			t.Fatalf("answer %d is outside the closed vocabulary", int(got))
		}
		// Determinism: the same bytes must always produce the same answer, because
		// the answer decides whether a tool call is released.
		if again := pathvirtualization.ScanReservedAlias(raw); again != got {
			t.Fatalf("non-deterministic answer: %v then %v", got, again)
		}
		// The marker is absent from the answer vocabulary by construction, so a
		// payload with no marker bytes at all must always be absent.
		if !bytes.Contains(raw, []byte(".__aip_v1__")) && got != pathvirtualization.ReservedAliasAbsent {
			t.Fatalf("marker-free payload reported %v", got)
		}
		// A payload carrying a complete alias of a derived mapping must always be
		// recognized: this is the property the caller relies on to refuse closed, so
		// a false negative here would release the reserved namespace.
		mapping, reason := pathvirtualization.DeriveMapping(scanPOSIXRoot)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("derive fixture root: %v", reason)
		}
		document := append(bytes.Repeat([]byte("x"), len(raw)), mapping.VirtualRoot...)
		document = append(document, "src/main.go"...)
		if got := pathvirtualization.ScanReservedAlias(document); got != pathvirtualization.ReservedAliasWellFormed {
			t.Fatalf("a document carrying a derived alias reported %v", got)
		}
	})
}

// TestScanReservedAliasAcceptsUppercaseHexDigitsInEscapes pins the decoder half of the
// escaped-spelling pass: JSON spells the digits of a \uXXXX escape in either case, so a
// marker whose escapes use the upper-case A-F spelling is still the reserved namespace.
//
// The false negative is the dangerous direction. This scan is the last recognizer in front
// of a completed argument document the runtime could not read, so a payload that spells an
// alias with these escapes and is not recognized here is released to the client, which is
// the release design.md "Error Handling" forbids.
func TestScanReservedAliasAcceptsUppercaseHexDigitsInEscapes(t *testing.T) {
	t.Parallel()

	// The fixed marker spelled as escapes whose hexadecimal digit letters are upper case:
	// \u002E \u005F \u005F \u006C \u0069 \u0070 \u005F \u0076 \u0031 \u005F \u005F.
	escaped := `{"file_path":"/\u002E\u005F\u005F\u0061\u0069\u0070\u005F\u0076\u0031\u005F\u005F/w_` +
		scanTag + `/src/a.go"`
	if got := pathvirtualization.ScanReservedAlias([]byte(escaped)); got != pathvirtualization.ReservedAliasWellFormed {
		t.Fatalf("an escaped marker with upper-case hex digits: presence = %v, want well-formed", got)
	}
}

// TestScanReservedAliasCopiesMalformedEscapesThrough pins the other half of the
// escaped-spelling pass: an escape that is not a complete hexadecimal quad, or that
// spells an unpaired surrogate, is copied through unchanged (see unescapeJSONProjection).
// The unescaped projection may therefore only ever JOIN bytes that a valid escape
// denotes; a malformed escape must not manufacture a marker the payload does not spell,
// which would refuse an ordinary call with a malformed_tag reason.
//
// Every fixture splits the fixed marker around the malformed escape, so the assertion
// fails the moment the projection consumes the escape instead of keeping it.
func TestScanReservedAliasCopiesMalformedEscapesThrough(t *testing.T) {
	t.Parallel()

	const (
		markerPrefix = `/.__aip`
		markerSuffix = `_v1__`
	)
	for _, tc := range []struct {
		name   string
		escape string
	}{
		{name: "non_hexadecimal_digit", escape: `\uZZZZ`},
		{name: "incomplete_quad", escape: `\u005_`},
		{name: "unpaired_high_surrogate", escape: `\ud83d`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := `{"file_path":"` + markerPrefix + tc.escape + markerSuffix + `/w_` + scanTag + `/src/a.go"}`
			if got := pathvirtualization.ScanReservedAlias([]byte(raw)); got != pathvirtualization.ReservedAliasAbsent {
				t.Errorf("scan(%q)=%v want absent: a malformed escape must not manufacture a marker the payload does not spell", raw, got)
			}
		})
	}
}
