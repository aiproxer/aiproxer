package bedrock_test

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBackendMalformedEventHeaderReturnsError(t *testing.T) {
	t.Parallel()
	// A CRC-valid AWS event-stream frame isolates the unsupported header
	// value type from checksum or framing errors. The SDK reader must return
	// a request error instead of panicking on its background goroutine.
	frame := make([]byte, 19)
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], 3)
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:15], []byte{1, 'x', 255})
	binary.BigEndian.PutUint32(frame[15:], crc32.ChecksumIEEE(frame[:15]))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(frame)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	be := backend.NewWithContext(ctx, backend.Config{
		Region: "us-east-1", AccessKeyID: "test-access-key", SecretAccessKey: "test-secret",
		BaseEndpoint: srv.URL, DisableHTTPS: true, HTTPClient: srv.Client(),
	})
	call := aipapi.Call{
		ID:       "malformed-event-header",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	stream, err := be.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "test-model"},
	})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	_, err = aipapi.Collect(ctx, stream)
	if err == nil || !strings.Contains(err.Error(), "unknown value type 255") {
		t.Fatalf("expected unsupported event-header decoding error, got %v", err)
	}
}
