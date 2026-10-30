package modelsdev

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

const snapshotEnvelopeVersion = 1

// diskEnvelope is the on-disk format for a validated catalog snapshot.
// Payload is base64-encoded raw catalog JSON so the content hash matches the exact upstream bytes.
type diskEnvelope struct {
	Version     int       `json:"version"`
	Generation  string    `json:"generation"`
	ContentHash string    `json:"content_hash"`
	FetchedAt   time.Time `json:"fetched_at"`
	PayloadB64  string    `json:"payload_b64"`
}

// FileSnapshotStore implements [modelcatalog.SnapshotCache] with a single JSON envelope file.
type FileSnapshotStore struct {
	path string
	// fileSync and dirSync are the durability primitives Save relies on:
	// snapshot bytes are flushed before the rename publishes them, and the
	// directory entry is flushed after. They are struct fields so tests can
	// prove the ordering and failure handling without a mutable package seam.
	fileSync func(*os.File) error
	dirSync  func(string) error
}

var _ modelcatalog.SnapshotCache = (*FileSnapshotStore)(nil)

// NewFileSnapshotStore returns a cache adapter that reads and writes path atomically.
func NewFileSnapshotStore(path string) *FileSnapshotStore {
	return &FileSnapshotStore{
		path:     path,
		fileSync: func(f *os.File) error { return f.Sync() },
		dirSync:  syncDirectory,
	}
}

// Load reads and validates the local snapshot file.
func (f *FileSnapshotStore) Load(ctx context.Context) (modelcatalog.Snapshot, error) {
	var zero modelcatalog.Snapshot
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return zero, fmt.Errorf("modelsdev cache: %w", err)
		}
		return zero, fmt.Errorf("modelsdev cache read: %w", err)
	}
	var env diskEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, fmt.Errorf("modelsdev cache decode: %w", err)
	}
	if env.Version != snapshotEnvelopeVersion {
		return zero, fmt.Errorf("modelsdev cache: unsupported envelope version %d", env.Version)
	}
	payload, err := base64.StdEncoding.DecodeString(env.PayloadB64)
	if err != nil {
		return zero, fmt.Errorf("modelsdev cache: payload decode: %w", err)
	}
	if len(payload) == 0 {
		return zero, errors.New("modelsdev cache: empty payload")
	}
	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if env.ContentHash == "" || got != env.ContentHash {
		return zero, errors.New("modelsdev cache: content hash mismatch")
	}
	snap, err := ParseSnapshot(payload, env.FetchedAt)
	if err != nil {
		return zero, fmt.Errorf("modelsdev cache parse: %w", err)
	}
	if snap.ContentHash != env.ContentHash || snap.Generation != env.Generation {
		return zero, errors.New("modelsdev cache: parsed snapshot metadata mismatch")
	}
	return snap, nil
}

// Save writes a validated snapshot to disk atomically and durably: the
// temp-file bytes are flushed before the rename publishes them, and the
// directory entry is flushed after, so a crash cannot lose a successful save
// or publish a torn file.
func (f *FileSnapshotStore) Save(ctx context.Context, snapshot modelcatalog.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(snapshot.WirePayload) == 0 {
		return errors.New("modelsdev cache save: snapshot has no wire payload")
	}
	sum := sha256.Sum256(snapshot.WirePayload)
	hash := hex.EncodeToString(sum[:])
	if snapshot.ContentHash != hash || snapshot.Generation != hash {
		return errors.New("modelsdev cache save: snapshot metadata inconsistent with wire payload")
	}
	env := diskEnvelope{
		Version:     snapshotEnvelopeVersion,
		Generation:  snapshot.Generation,
		ContentHash: snapshot.ContentHash,
		FetchedAt:   snapshot.FetchedAt,
		PayloadB64:  base64.StdEncoding.EncodeToString(snapshot.WirePayload),
	}
	out, err := json.MarshalIndent(&env, "", "  ")
	if err != nil {
		return fmt.Errorf("modelsdev cache marshal: %w", err)
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("modelsdev cache mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("modelsdev cache temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("modelsdev cache write: %w", err)
	}
	if err := f.fileSync(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("modelsdev cache sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelsdev cache close temp: %w", err)
	}
	if err := os.Rename(tmpPath, f.path); err != nil {
		return fmt.Errorf("modelsdev cache rename: %w", err)
	}
	if err := f.dirSync(dir); err != nil {
		return fmt.Errorf("modelsdev cache directory sync: %w", err)
	}
	return nil
}
