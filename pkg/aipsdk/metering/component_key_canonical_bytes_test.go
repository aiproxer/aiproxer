package metering_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestComponentKeyCanonicalBytesMatchesLegacyEncoding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		key          metering.ComponentKey
		wantExact    string
		wantContains []string
	}{
		{
			name: "ordinary key",
			key: metering.ComponentKey{
				Direction: metering.DirectionOutput,
				Component: metering.ComponentOutputToken,
				Unit:      metering.UnitToken,
			},
			wantExact: `{"direction":"output","component":"output_token","unit":"token"}`,
		},
		{
			name: "empty dimensions remain omitted",
			key: metering.ComponentKey{
				Direction:  metering.DirectionOutput,
				Component:  metering.ComponentOutputToken,
				Unit:       metering.UnitToken,
				Dimensions: []metering.Dimension{},
			},
			wantExact: `{"direction":"output","component":"output_token","unit":"token"}`,
		},
		{
			name: "aliased direction sorted dimensions and JSON escaping",
			key: metering.ComponentKey{
				Direction: metering.EconomicDirection(metering.DirectionInput),
				Component: metering.ComponentImage,
				Unit:      metering.UnitImage,
				SchemaID:  "provider:image:v1",
				Dimensions: []metering.Dimension{
					{Name: "unicode", Value: "é雪"},
					{Name: "markup", Value: "<>&"},
				},
			},
			wantContains: []string{
				`"name":"markup"`,
				`\u003c\u003e\u0026`,
				"é雪",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			originalDimensions := cloneComponentDimensions(tc.key.Dimensions)
			originalWasNil := tc.key.Dimensions == nil
			want := legacyComponentKeyEncoding(t, tc.key)
			got := tc.key.CanonicalBytes()
			if !bytes.Equal(got, want) {
				t.Fatalf("CanonicalBytes() = %s, legacy encoding = %s", got, want)
			}
			if tc.wantExact != "" && string(got) != tc.wantExact {
				t.Fatalf("CanonicalBytes() = %s, want exact wire JSON %s", got, tc.wantExact)
			}
			for _, fragment := range tc.wantContains {
				if !strings.Contains(string(got), fragment) {
					t.Errorf("CanonicalBytes() %s does not contain %q", got, fragment)
				}
			}

			canonicalJSON, err := tc.key.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canonicalJSON, want) {
				t.Fatalf("CanonicalJSON() = %s, legacy encoding = %s", canonicalJSON, want)
			}
			if gotKey := tc.key.CanonicalKey(); gotKey != string(want) {
				t.Errorf("CanonicalKey() = %q, want %q", gotKey, want)
			}
			sum := sha256.Sum256(want)
			if gotFingerprint, wantFingerprint := tc.key.Fingerprint(), hex.EncodeToString(sum[:]); gotFingerprint != wantFingerprint {
				t.Errorf("Fingerprint() = %q, want %q", gotFingerprint, wantFingerprint)
			}
			if !reflect.DeepEqual(tc.key.Dimensions, originalDimensions) || (tc.key.Dimensions == nil) != originalWasNil {
				t.Errorf("canonicalization mutated caller dimensions: got %#v, want %#v", tc.key.Dimensions, originalDimensions)
			}
		})
	}
}

func TestComponentKeyCanonicalIdentityIgnoresDimensionOrder(t *testing.T) {
	t.Parallel()

	first := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "provider:image:v1",
		Dimensions: []metering.Dimension{
			{Name: "quality", Value: "high"},
			{Name: "width", Value: "1024"},
		},
	}
	second := first
	second.Dimensions = []metering.Dimension{
		{Name: "width", Value: "1024"},
		{Name: "quality", Value: "high"},
	}

	want := legacyComponentKeyEncoding(t, first)
	if got := first.CanonicalBytes(); !bytes.Equal(got, want) {
		t.Fatalf("CanonicalBytes() = %s, legacy encoding = %s", got, want)
	}
	if got := second.CanonicalBytes(); !bytes.Equal(got, want) {
		t.Fatalf("reordered CanonicalBytes() = %s, want %s", got, want)
	}
	if first.CanonicalKey() != second.CanonicalKey() {
		t.Fatal("dimension order changed canonical identity")
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("dimension order changed canonical fingerprint")
	}
}

func TestComponentKeyCanonicalBytesPreserveInvalidKeyBehavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  metering.ComponentKey
	}{
		{
			name: "duplicate dimensions",
			key: metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: metering.ComponentImage,
				Unit:      metering.UnitImage,
				Dimensions: []metering.Dimension{
					{Name: "quality", Value: "high"},
					{Name: "quality", Value: "low"},
				},
			},
		},
		{
			name: "invalid UTF-8 component",
			key: metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: "custom:" + string([]byte{0xff}),
				Unit:      metering.UnitByte,
				SchemaID:  "provider:custom:v1",
			},
		},
		{
			name: "invalid UTF-8 dimension value",
			key: metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: metering.ComponentImage,
				Unit:      metering.UnitImage,
				Dimensions: []metering.Dimension{
					{Name: "quality", Value: string([]byte{0xfe})},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			originalDimensions := cloneComponentDimensions(tc.key.Dimensions)
			originalWasNil := tc.key.Dimensions == nil
			if got := tc.key.CanonicalBytes(); got != nil {
				t.Errorf("CanonicalBytes() = %s, want nil", got)
			}
			if got := tc.key.CanonicalKey(); got != "" {
				t.Errorf("CanonicalKey() = %q, want empty string", got)
			}
			if got := tc.key.Fingerprint(); got != "" {
				t.Errorf("Fingerprint() = %q, want empty string", got)
			}
			if _, err := tc.key.CanonicalJSON(); !errors.Is(err, metering.ErrInvalidComponentKey) {
				t.Errorf("CanonicalJSON() error = %v, want ErrInvalidComponentKey", err)
			}
			if !reflect.DeepEqual(tc.key.Dimensions, originalDimensions) || (tc.key.Dimensions == nil) != originalWasNil {
				t.Errorf("failed canonicalization mutated caller dimensions: got %#v, want %#v", tc.key.Dimensions, originalDimensions)
			}
		})
	}
}

func legacyComponentKeyEncoding(t *testing.T, key metering.ComponentKey) []byte {
	t.Helper()

	normalized, err := key.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json.Marshal(normalized key) error = %v", err)
	}
	return encoded
}

func cloneComponentDimensions(dimensions []metering.Dimension) []metering.Dimension {
	if dimensions == nil {
		return nil
	}
	cloned := make([]metering.Dimension, len(dimensions))
	copy(cloned, dimensions)
	return cloned
}
