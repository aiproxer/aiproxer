package testkit

import (
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewStubExecutorWithSecureSession_smoke(t *testing.T) {
	t.Parallel()
	var captured sync.Map
	ex := NewStubExecutorWithSecureSession(t, SecureSessionStubExecutorOptions{}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), &captured)
	if ex == nil || ex.SecureSession == nil {
		t.Fatal("expected secure-session wiring")
	}
	if ex.SessionDenialMapper == nil {
		t.Fatal("expected denial mapper")
	}
}
