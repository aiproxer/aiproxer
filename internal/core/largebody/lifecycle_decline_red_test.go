package largebody_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestFindingM4_PreExistingReaderClosesAfterComplete_SpillFileLeaked proves Finding M4:
// SpillBuffer.Open increments b.activeReaders, but Complete copies activeReaders into
// a newly constructed CompletedSource. When the pre-existing reader closes, its defer
// calls b.readerClosed() on the parent SpillBuffer instead of the CompletedSource.
// Thus, CompletedSource.activeReaders is never decremented, leaving deletePending=true
// and the temporary spill file leaked on disk.
func TestFindingM4_PreExistingReaderClosesAfterComplete_SpillFileLeaked(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64, // 64-byte memory ceiling forces spill
		CopyBufferSize:   128,
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	require.NoError(t, err)

	// Write more than MemorySpoolBytes so data spills to disk
	payload := bytes.Repeat([]byte("spill-chunk-payload-data-"), 10)
	n, err := buf.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.True(t, buf.HasSpilled(), "buffer must be backed by temporary file")

	// 1. Open reader BEFORE Complete
	oldReader, err := buf.Open()
	require.NoError(t, err)

	// 2. Complete transitions SpillBuffer to CompletedSource
	compSrc, err := buf.Complete()
	require.NoError(t, err)

	spillFile := compSrc.FilePath()
	require.NotEmpty(t, spillFile, "expected non-empty spill file path")
	require.FileExists(t, spillFile, "spill file must exist on disk")

	// 3. Close CompletedSource while old reader is still open
	// Deletion must be marked pending because a reader is still active
	err = compSrc.Close()
	require.NoError(t, err)
	require.FileExists(t, spillFile, "spill file must not be removed while old reader is active")

	// 4. Close the pre-existing reader
	// Invariant: once the last active reader closes, the spill file must be removed
	err = oldReader.Close()
	require.NoError(t, err)

	// Under current code, oldReader.Close calls buf.readerClosed() which decrements buf.activeReaders,
	// leaving compSrc.activeReaders untouched at 1. The spill file is never deleted.
	assert.NoFileExists(t, spillFile, "spill file must be deleted when the last reader closes, but was leaked")
}

// TestFindingM5_ForcedNewCaptureReaderFailure_YieldsTerminalErrorNeverDeclinedNil proves Finding M5:
// When NewCaptureReader fails during capture decline (e.g. opening retained prefix fails),
// CaptureRequestBody at lines 412, 451, 472 ignores the error with `cont, _ := NewCaptureReader(...)`
// and returns CaptureOutcomeDeclined with Continuation == nil.
// This violates the contract at capture.go:356-366 (recoverable decline returns a lossless continuation).
// A forced NewCaptureReader failure must yield a terminal failure, NEVER CaptureOutcomeDeclined with nil continuation.
func TestFindingM5_ForcedNewCaptureReaderFailure_YieldsTerminalErrorNeverDeclinedNil(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	injectedOpenErr := errors.New("injected spill open error")

	cfg := largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64,
		OpenFile: func(path string) (io.ReadCloser, error) {
			return nil, injectedOpenErr
		},
	}

	buf, err := largebody.NewSpillBuffer(cfg)
	require.NoError(t, err)
	defer func() { _ = buf.Close() }()

	// Write enough so that bytes are spilled and BytesWritten > 0
	prefix := bytes.Repeat([]byte("retained-prefix-bytes-"), 10)
	_, err = buf.Write(prefix)
	require.NoError(t, err)
	require.True(t, buf.HasSpilled(), "expected buffer to have spilled to file")

	// Run CaptureRequestBody with an OnChunk callback that rejects the next chunk
	clientBody := io.NopCloser(bytes.NewReader([]byte(`{"model":"gpt-4o"}`)))
	captureCfg := largebody.CaptureConfig{
		MaxBytes: 1 << 20,
		OnChunk: func(chunk []byte) error {
			return errors.New("streaming scanner syntax error")
		},
	}

	res := largebody.CaptureRequestBody(clientBody, buf, captureCfg)

	// Contract: CaptureOutcomeDeclined must always provide a non-nil lossless continuation reader.
	// If continuation reader creation fails, the outcome cannot be Declined; it must be terminal error.
	if res.Outcome == largebody.CaptureOutcomeDeclined {
		assert.NotNil(t, res.Continuation, "CaptureOutcomeDeclined must never have a nil Continuation")
	}
	assert.NotEqual(t, largebody.CaptureOutcomeDeclined, res.Outcome,
		"forced NewCaptureReader failure must yield terminal failure, not CaptureOutcomeDeclined with nil continuation")
	assert.ErrorIs(t, res.Err, injectedOpenErr,
		"expected returned terminal error to carry underlying NewCaptureReader failure")
}
