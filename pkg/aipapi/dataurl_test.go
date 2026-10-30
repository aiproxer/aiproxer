package aipapi

import "testing"

func TestStripDataURLBase64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		wantMime string
		wantB64  string
		wantOK   bool
	}{
		{
			name:     "valid_png",
			in:       "data:image/png;base64,iVBORw0K",
			wantMime: "image/png",
			wantB64:  "iVBORw0K",
			wantOK:   true,
		},
		{
			name:   "missing_data_prefix",
			in:     "image/png;base64,iVBORw0K",
			wantOK: false,
		},
		{
			name:   "missing_semicolon",
			in:     "data:image/png",
			wantOK: false,
		},
		{
			name:   "missing_base64_marker",
			in:     "data:image/png;raw,iVBORw0K",
			wantOK: false,
		},
		{
			name:   "empty",
			in:     "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mime, b64, ok := StripDataURLBase64(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if mime != tt.wantMime || b64 != tt.wantB64 {
				t.Fatalf("got (%q,%q) want (%q,%q)", mime, b64, tt.wantMime, tt.wantB64)
			}
		})
	}
}
