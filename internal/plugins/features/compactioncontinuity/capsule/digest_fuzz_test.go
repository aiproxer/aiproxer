package capsule

import "testing"

func FuzzParseNeverPanics(f *testing.F) {
	e, err := New("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err == nil {
		data, marshalErr := marshalCanonical(e)
		if marshalErr == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = Parse(data) })
}
