package jsonprobe

import "testing"

func TestJSONRequestMatchersUseJSONStructure(t *testing.T) {
	t.Parallel()
	body := []byte(`{"metadata":"temperature:0.11","nested":{"temperature":0.11},"tools":[]}`)
	if !HasJSONNumber(body, "temperature", 0.11) {
		t.Fatal("expected nested numeric temperature")
	}
	if !HasJSONKey(body, "tools") {
		t.Fatal("expected tools key")
	}
	if HasJSONNumber([]byte(`{"temperature":"0.11"}`), "temperature", 0.11) {
		t.Fatal("string value must not match numeric request control")
	}
	if HasJSONNumber([]byte(`{"prompt":"temperature":0.11}`), "temperature", 0.11) {
		t.Fatal("text content must not match request control")
	}
}

func TestJSONRequestMatchersRejectMalformedBody(t *testing.T) {
	t.Parallel()
	if HasJSONKey([]byte(`{"tools":`), "tools") || HasJSONNumber([]byte(`{"temperature":0.11`), "temperature", 0.11) {
		t.Fatal("malformed JSON must not activate a scripted response")
	}
}
