package accessmode

import (
	"errors"
	"testing"
)

func TestClassifyListenAddress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want Surface
	}{
		{raw: "", want: SurfaceMalformed},
		{raw: "127.0.0.1", want: SurfaceLoopback},
		{raw: "127.0.0.1:8080", want: SurfaceLoopback},
		{raw: "127.42.1.2:9", want: SurfaceLoopback},
		{raw: "[::1]:8080", want: SurfaceLoopback},
		{raw: "localhost:8080", want: SurfaceLoopback},
		{raw: ":8080", want: SurfaceBroad},
		{raw: "0.0.0.0:8080", want: SurfaceBroad},
		{raw: "[::]:8080", want: SurfaceBroad},
		{raw: "192.168.1.1:8080", want: SurfaceNonLoopback},
		{raw: "example.test:443", want: SurfaceNonLoopback},
		{raw: "not a port", want: SurfaceMalformed},
		{raw: ":::9", want: SurfaceMalformed},
	} {
		got, err := ClassifyListenAddress(tc.raw)
		if tc.want == SurfaceMalformed {
			if !errors.Is(err, ErrMalformedListenAddress) {
				t.Fatalf("raw %q: want %v, got %v; surface %q", tc.raw, ErrMalformedListenAddress, err, got.Surface)
			}
		} else {
			if err != nil {
				t.Fatalf("raw %q: unexpected err %v", tc.raw, err)
			}
		}
		if got.Surface != tc.want {
			t.Fatalf("raw %q: want surface %q got %q (host=%q port=%q)", tc.raw, tc.want, got.Surface, got.Host, got.Port)
		}
	}
}
