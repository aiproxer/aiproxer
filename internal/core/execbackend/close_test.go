package execbackend

import "testing"

func TestBackend_CloseOptionalNilNoOp(t *testing.T) {
	t.Parallel()
	var zero Backend
	if zero.Close != nil {
		t.Fatal("zero-value Backend.Close must be nil")
	}
	called := false
	be := Backend{
		Close: func() error {
			called = true
			return nil
		},
	}
	if be.Close == nil {
		t.Fatal("expected non-nil Close callback")
	}
	if err := be.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !called {
		t.Fatal("expected Close callback to run")
	}
}
