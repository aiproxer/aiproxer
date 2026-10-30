package state

import (
	"encoding/json"
	"testing"
)

func TestMemoRef_IsEmptyAndEqual(t *testing.T) {
	t.Parallel()

	var zeroRef MemoRef
	if !zeroRef.IsEmpty() {
		t.Fatal("zero MemoRef must be empty")
	}
	a := MemoRef{Key: "k1", Version: 3}
	if a.IsEmpty() {
		t.Fatal("non-zero MemoRef must not be empty")
	}
	if !a.Equal(MemoRef{Key: "k1", Version: 3}) {
		t.Fatal("equal MemoRefs must compare equal")
	}
	if a.Equal(MemoRef{Key: "k1", Version: 4}) {
		t.Fatal("different version must not compare equal")
	}
	if a.Equal(MemoRef{Key: "k2", Version: 3}) {
		t.Fatal("different key must not compare equal")
	}
}

func TestMemoRef_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	r := MemoRef{Key: "abc", Version: 9}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got MemoRef
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Equal(r) {
		t.Fatalf("got %+v want %+v", got, r)
	}
}
