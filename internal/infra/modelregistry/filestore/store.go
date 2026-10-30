package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

type Store struct {
	path string
	// fileSync and dirSync are the durability primitives Save relies on:
	// snapshot bytes are flushed before the rename publishes them, and the
	// directory entry is flushed after. They are struct fields so tests can
	// prove the ordering and failure handling without a mutable package seam.
	fileSync func(*os.File) error
	dirSync  func(string) error
}

func New(path string) *Store {
	return &Store{
		path:     path,
		fileSync: func(f *os.File) error { return f.Sync() },
		dirSync:  syncDirectory,
	}
}

func (s *Store) Load(ctx context.Context) (modelregistry.Snapshot, error) {
	if ctx == nil {
		return modelregistry.Snapshot{}, modelregistry.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelregistry.Snapshot{}, err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return modelregistry.Snapshot{}, modelregistry.ErrSnapshotUnavailable
		}
		return modelregistry.Snapshot{}, fmt.Errorf("modelregistry cache load: %w", err)
	}
	var snap modelregistry.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return modelregistry.Snapshot{}, fmt.Errorf("modelregistry cache decode: %w", err)
	}
	if snap.Models == nil {
		snap.Models = []modelregistry.BackendModel{}
	}
	return snap, nil
}

// Save publishes the snapshot atomically and durably: the temp-file bytes are
// flushed before the rename publishes them, and the directory entry is flushed
// after, so a crash leaves either the previous snapshot or the complete new one.
func (s *Store) Save(ctx context.Context, snap modelregistry.Snapshot) error {
	if ctx == nil {
		return modelregistry.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("modelregistry cache mkdir: %w", err)
	}
	if snap.Models == nil {
		snap.Models = []modelregistry.BackendModel{}
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("modelregistry cache encode: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("modelregistry cache temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("modelregistry cache write: %w", err)
	}
	if err := s.fileSync(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("modelregistry cache sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelregistry cache close: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("modelregistry cache replace: %w", err)
	}
	if err := s.dirSync(dir); err != nil {
		return fmt.Errorf("modelregistry cache directory sync: %w", err)
	}
	return nil
}
