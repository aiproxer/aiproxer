package codex

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicReplaceOps are the durability primitives that publish a file
// atomically. They are explicit fields rather than direct calls so tests can
// prove the ordering and failure behavior of each step without a global seam.
type atomicReplaceOps struct {
	syncFile func(*os.File) error
	syncDir  func(string) error
	rename   func(oldpath, newpath string) error
}

// defaultAtomicReplaceOps returns the production primitives.
func defaultAtomicReplaceOps() atomicReplaceOps {
	return atomicReplaceOps{
		syncFile: func(f *os.File) error { return f.Sync() },
		syncDir:  syncDirectory,
		rename:   os.Rename,
	}
}

// atomicReplaceFile replaces path with data without ever exposing a partially
// written file. The bytes are flushed to stable storage before the rename
// publishes them, and the directory entry is flushed after, so a crash leaves
// either the previous file or the complete new one. A failed flush or rename
// is reported to the caller instead of being repaired by an in-place write,
// which could truncate the live file.
func atomicReplaceFile(path string, data []byte, ops atomicReplaceOps) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %q: %w", path, err)
	}
	if err := ops.syncFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %q: %w", path, err)
	}
	if err := ops.rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	if err := ops.syncDir(dir); err != nil {
		return fmt.Errorf("sync directory for %q: %w", path, err)
	}
	return nil
}
