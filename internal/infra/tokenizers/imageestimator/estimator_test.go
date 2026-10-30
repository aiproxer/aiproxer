package imageestimator

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestCountLowAndAutoReturnBaseTokensForPNGDataURI(t *testing.T) {
	t.Parallel()
	estimator := New(Config{BaseTokens: 91})
	ref := pngDataURI(t, 16, 8)

	for _, detail := range []string{"low", "auto", ""} {
		t.Run("detail_"+detail, func(t *testing.T) {
			t.Parallel()
			got, err := estimator.Count(Input{Ref: ref, Detail: detail})
			if err != nil {
				t.Fatalf("Count() error = %v", err)
			}
			if got != 91 {
				t.Fatalf("Count() = %d, want 91", got)
			}
		})
	}
}

func TestCountHighDetailUsesTileMath(t *testing.T) {
	t.Parallel()
	estimator := New(Config{BaseTokens: 85})

	got, err := estimator.Count(Input{Ref: pngDataURI(t, 1024, 1024), Detail: "high"})
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if got != 765 {
		t.Fatalf("Count() = %d, want 765", got)
	}
}

func TestCountUnsupportedDetailReturnsErrUnavailable(t *testing.T) {
	t.Parallel()
	estimator := New(Config{})

	_, err := estimator.Count(Input{Ref: pngDataURI(t, 16, 16), Detail: "medium"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Count() error = %v, want ErrUnavailable", err)
	}
}

func TestCountNonDataURIUsesConfiguredDefaultOnly(t *testing.T) {
	t.Parallel()
	_, err := New(Config{}).Count(Input{Ref: "https://example.invalid/image.png", Detail: "low"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Count() error = %v, want ErrUnavailable", err)
	}

	got, err := New(Config{UseDefaultTokens: true, DefaultTokens: 123}).Count(Input{
		Ref:    "https://example.invalid/image.png",
		Detail: "low",
	})
	if err != nil {
		t.Fatalf("Count() with default error = %v", err)
	}
	if got != 123 {
		t.Fatalf("Count() with default = %d, want 123", got)
	}
}

func TestCountInvalidDataReturnsErrUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  string
		cfg  Config
	}{
		{name: "invalid_base64", ref: "data:image/png;base64,not-base64!"},
		{name: "non_image", ref: dataURI("image/png", []byte("not an image"))},
		{name: "oversized_payload", ref: dataURI("image/png", []byte("12345")), cfg: Config{MaxDecodedBytes: 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tt.cfg).Count(Input{Ref: tt.ref, Detail: "low"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Count() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func pngDataURI(t *testing.T, width, height int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return dataURI("image/png", buf.Bytes())
}

func dataURI(mediaType string, data []byte) string {
	var b strings.Builder
	b.WriteString("data:")
	b.WriteString(mediaType)
	b.WriteString(";base64,")
	b.WriteString(base64.StdEncoding.EncodeToString(data))
	return b.String()
}
