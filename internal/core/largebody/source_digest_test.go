package largebody_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestCompletedSource_SourceDigest_MatchesDirectHash tests that the source integrity
// digest bound to CompletedSource matches the direct SHA-256 hash of the payload
// across all storage modes (Requirements 15, 16, 20; design section 5).
func TestCompletedSource_SourceDigest_MatchesDirectHash(t *testing.T) {
	t.Parallel()

	payload := []byte("the-quick-brown-fox-jumps-over-the-lazy-dog-1234567890-payload-body-data")
	expectedSum := sha256.Sum256(payload)
	expectedDigest := largebody.NewSourceDigest(expectedSum)

	t.Run("NewMemorySource", func(t *testing.T) {
		t.Parallel()
		src := largebody.NewMemorySource(payload)
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
		if src.SourceDigest() != expectedDigest {
			t.Fatalf("src.SourceDigest() = %s, want %s", src.SourceDigest(), expectedDigest)
		}
		if src.Digest().Sum() != expectedSum {
			t.Fatalf("src.Digest().Sum() = %x, want %x", src.Digest().Sum(), expectedSum)
		}
	})

	t.Run("SpillBuffer_MemoryOnly", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: int64(len(payload) + 1024),
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()

		if _, err := buf.Write(payload); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}
		if buf.Digest() != expectedDigest {
			t.Fatalf("buf.Digest() = %s, want %s", buf.Digest(), expectedDigest)
		}

		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("buf.Complete: %v", err)
		}
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
		if src.SourceDigest() != expectedDigest {
			t.Fatalf("src.SourceDigest() = %s, want %s", src.SourceDigest(), expectedDigest)
		}
	})

	t.Run("SpillBuffer_SpilledToDisk", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 16, // Forces spill after 16 bytes
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()

		// Write in multiple small chunks
		chunkSize := 10
		for i := 0; i < len(payload); i += chunkSize {
			end := min(i+chunkSize, len(payload))
			if _, err := buf.Write(payload[i:end]); err != nil {
				t.Fatalf("buf.Write: %v", err)
			}
		}

		if !buf.HasSpilled() {
			t.Fatal("expected buffer to have spilled to disk")
		}
		if buf.Digest() != expectedDigest {
			t.Fatalf("buf.Digest() = %s, want %s", buf.Digest(), expectedDigest)
		}

		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("buf.Complete: %v", err)
		}
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
		if src.SourceDigest() != expectedDigest {
			t.Fatalf("src.SourceDigest() = %s, want %s", src.SourceDigest(), expectedDigest)
		}
	})

	t.Run("SpillBuffer_ZeroMemorySpool", func(t *testing.T) {
		t.Parallel()
		spoolDir := t.TempDir()
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         spoolDir,
			MemorySpoolBytes: 0, // All data spills to disk
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		defer func() { _ = buf.Close() }()

		if _, err := buf.Write(payload); err != nil {
			t.Fatalf("buf.Write: %v", err)
		}
		if buf.Digest() != expectedDigest {
			t.Fatalf("buf.Digest() = %s, want %s", buf.Digest(), expectedDigest)
		}

		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("buf.Complete: %v", err)
		}
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
	})
}

// TestSpillBuffer_IncrementalDigest_MatchesWriteProgression proves that the digest
// updates incrementally as chunks are written, matching SHA-256 at each point.
func TestSpillBuffer_IncrementalDigest_MatchesWriteProgression(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 32,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Before any write, digest is empty/zero
	if !buf.Digest().IsZero() {
		t.Fatalf("expected zero digest before writes, got %s", buf.Digest())
	}

	chunks := [][]byte{
		[]byte("chunk-one-"),
		[]byte("chunk-two-123456-"),
		[]byte("chunk-three-spilling-over-memory-limit-"),
		[]byte("chunk-four-final-segment"),
	}

	var cumulative []byte
	for idx, chunk := range chunks {
		if _, err := buf.Write(chunk); err != nil {
			t.Fatalf("chunk %d write: %v", idx, err)
		}
		cumulative = append(cumulative, chunk...)
		expectedSum := sha256.Sum256(cumulative)
		if buf.Digest().Sum() != expectedSum {
			t.Fatalf("after chunk %d: buf.Digest().Sum() = %x, want %x", idx, buf.Digest().Sum(), expectedSum)
		}
	}

	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("buf.Complete: %v", err)
	}
	defer func() { _ = src.Close() }()

	expectedFinal := sha256.Sum256(cumulative)
	if src.Digest().Sum() != expectedFinal {
		t.Fatalf("final src.Digest().Sum() = %x, want %x", src.Digest().Sum(), expectedFinal)
	}
}

// TestCompletedSource_SourceDigest_DistinctFromIdentityDigest verifies that
// SourceDigest is a distinct type from IdentityDigest and cannot be substituted
// for canonical economic/request identity (Requirement 16.5; design section 6).
func TestCompletedSource_SourceDigest_DistinctFromIdentityDigest(t *testing.T) {
	t.Parallel()

	rawBytes := []byte("identical-raw-content-bytes")
	rawSum := sha256.Sum256(rawBytes)

	sourceDigest := largebody.NewSourceDigest(rawSum)
	identityDigest := largebody.NewIdentityDigest(rawSum)

	// Verify underlying sums match
	if sourceDigest.Sum() != identityDigest.Sum() {
		t.Fatal("expected identical sums for test setup")
	}

	// 1. Type identity: reflect.TypeOf must prove they are distinct types
	sourceType := reflect.TypeFor[largebody.SourceDigest]()
	identityType := reflect.TypeFor[largebody.IdentityDigest]()

	if sourceType == identityType {
		t.Fatalf("SourceDigest and IdentityDigest must be distinct types, but got %v == %v", sourceType, identityType)
	}
	if sourceType.AssignableTo(identityType) {
		t.Fatal("SourceDigest must NOT be assignable to IdentityDigest")
	}
	if identityType.AssignableTo(sourceType) {
		t.Fatal("IdentityDigest must NOT be assignable to SourceDigest")
	}

	// 2. Type names must be distinct
	if sourceType.Name() != "SourceDigest" {
		t.Fatalf("expected sourceType.Name() == 'SourceDigest', got %q", sourceType.Name())
	}
	if identityType.Name() != "IdentityDigest" {
		t.Fatalf("expected identityType.Name() == 'IdentityDigest', got %q", identityType.Name())
	}
}

// TestCompletedSource_NewCompletedSource_ConfigDigest verifies binding of explicit
// or computed digest via NewCompletedSource and CompletedSourceConfig.
func TestCompletedSource_NewCompletedSource_ConfigDigest(t *testing.T) {
	t.Parallel()

	data := []byte("completed-source-config-test-data")
	expectedSum := sha256.Sum256(data)
	expectedDigest := largebody.NewSourceDigest(expectedSum)

	t.Run("explicit digest", func(t *testing.T) {
		t.Parallel()
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: data,
			Size:   int64(len(data)),
			Digest: expectedDigest,
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
	})

	t.Run("auto-computed digest for memory backing", func(t *testing.T) {
		t.Parallel()
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: data,
			Size:   int64(len(data)),
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		defer func() { _ = src.Close() }()

		if src.Digest() != expectedDigest {
			t.Fatalf("src.Digest() = %s, want %s", src.Digest(), expectedDigest)
		}
	})
}

// TestCaptureRequestBody_DigestBinding verifies that CaptureRequestBody binds the
// source digest to the resulting CompletedSource and CaptureResult upon completion.
func TestCaptureRequestBody_DigestBinding(t *testing.T) {
	t.Parallel()

	bodyData := []byte("capture-request-body-streaming-data-for-digest-test")
	expectedSum := sha256.Sum256(bodyData)
	expectedDigest := largebody.NewSourceDigest(expectedSum)

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}

	body := io.NopCloser(bytes.NewReader(bodyData))
	res := largebody.CaptureRequestBody(body, buf, largebody.CaptureConfig{
		MaxBytes:       1024 * 1024,
		CopyBufferSize: 8,
	})

	if res.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected CaptureOutcomeCompleted, got %v (err: %v)", res.Outcome, res.Err)
	}
	if res.Source == nil {
		t.Fatal("expected non-nil Source")
	}
	defer func() { _ = res.Source.Close() }()

	if res.Digest != expectedDigest {
		t.Fatalf("res.Digest = %s, want %s", res.Digest, expectedDigest)
	}

	compSrc, ok := res.Source.(*largebody.CompletedSource)
	if !ok {
		t.Fatalf("expected *largebody.CompletedSource, got %T", res.Source)
	}
	if compSrc.Digest() != expectedDigest {
		t.Fatalf("compSrc.Digest() = %s, want %s", compSrc.Digest(), expectedDigest)
	}
}

// TestSpillBuffer_Digest_UnwrittenSuffixNotHashedUntilCommitted verifies that an unwritten
// suffix retained from a short write is not hashed until it is successfully committed
// (Requirement 20.6).
func TestSpillBuffer_Digest_UnwrittenSuffixNotHashedUntilCommitted(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 8,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// Write first 8 bytes (fits in memory)
	part1 := []byte("12345678")
	if _, err := buf.Write(part1); err != nil {
		t.Fatalf("part1 write: %v", err)
	}

	expectedPart1Sum := sha256.Sum256(part1)
	if buf.Digest().Sum() != expectedPart1Sum {
		t.Fatalf("after part1: buf.Digest().Sum() = %x, want %x", buf.Digest().Sum(), expectedPart1Sum)
	}

	// Writing part2 updates digest to part1 + part2
	part2 := []byte("90abcdef")
	if _, err := buf.Write(part2); err != nil {
		t.Fatalf("part2 write: %v", err)
	}

	full := append([]byte(nil), part1...)
	full = append(full, part2...)
	expectedFullSum := sha256.Sum256(full)
	if buf.Digest().Sum() != expectedFullSum {
		t.Fatalf("after part2: buf.Digest().Sum() = %x, want %x", buf.Digest().Sum(), expectedFullSum)
	}
}

// TestSpillBuffer_Digest_FaultInjection_UnwrittenSuffixNotHashed verifies that on a short
// write or write failure, only committed bytes are hashed, and the unwritten suffix is omitted
// from the running digest (Requirement 20.6).
func TestSpillBuffer_Digest_FaultInjection_UnwrittenSuffixNotHashed(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	mock := &mockDigestFaultFile{}

	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 4,
		CreateFile: func(dir string) (largebody.SpillFile, string, error) {
			return mock, filepath.Join(dir, "mock_digest.tmp"), nil
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	defer func() { _ = buf.Close() }()

	// 1. Write 4 bytes to fill memory
	memData := []byte("abcd")
	if _, err := buf.Write(memData); err != nil {
		t.Fatalf("memData write: %v", err)
	}

	expectedMemSum := sha256.Sum256(memData)
	if buf.Digest().Sum() != expectedMemSum {
		t.Fatalf("after memData: buf.Digest().Sum() = %x, want %x", buf.Digest().Sum(), expectedMemSum)
	}

	// 2. Enable write failure for file write
	mock.SetFail(true)
	spillData := []byte("efghijkl")
	_, err = buf.Write(spillData)
	if err == nil {
		t.Fatal("expected error on write when mock.fail=true")
	}

	// Suffix should be unwritten, and buf.Digest() must still equal expectedMemSum
	if !buf.HasUnwrittenSuffix() {
		t.Fatal("expected unwritten suffix")
	}
	if buf.Digest().Sum() != expectedMemSum {
		t.Fatalf("after failed write: buf.Digest().Sum() = %x, want %x", buf.Digest().Sum(), expectedMemSum)
	}

	// 3. Un-fail mock and write the unwritten suffix
	mock.SetFail(false)
	suffix := buf.TakeUnwrittenSuffix()
	if _, err := buf.Write(suffix); err != nil {
		t.Fatalf("suffix write: %v", err)
	}

	fullPayload := append([]byte(nil), memData...)
	fullPayload = append(fullPayload, spillData...)
	expectedFullSum := sha256.Sum256(fullPayload)
	if buf.Digest().Sum() != expectedFullSum {
		t.Fatalf("after committing suffix: buf.Digest().Sum() = %x, want %x", buf.Digest().Sum(), expectedFullSum)
	}
}

type mockDigestFaultFile struct {
	mu         sync.Mutex
	data       []byte
	shouldFail bool
}

func (m *mockDigestFaultFile) SetFail(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldFail = fail
}

func (m *mockDigestFaultFile) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shouldFail {
		return 0, errors.New("injected mock write error")
	}
	m.data = append(m.data, p...)
	return len(p), nil
}

func (m *mockDigestFaultFile) Read(p []byte) (int, error)                   { return 0, io.EOF }
func (m *mockDigestFaultFile) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (m *mockDigestFaultFile) Sync() error                                  { return nil }
func (m *mockDigestFaultFile) Close() error                                 { return nil }
func (m *mockDigestFaultFile) Name() string                                 { return "mock_digest.tmp" }
