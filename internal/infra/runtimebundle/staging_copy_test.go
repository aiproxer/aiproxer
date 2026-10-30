package runtimebundle

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Keep this serial: TotalAlloc is process-wide. Parallel test bodies in this
// package remain paused while this bounded fixture measurement runs.
func TestStagingCopyAllocationIsIndependentOfArtifactSize(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	target := filepath.Join(dir, "target.bin")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for range 512 {
		if _, err := file.Write(chunk); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := copyFileMode(target, source, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	t.Logf("32 MiB artifact copy allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("copying a 32 MiB artifact allocated %d bytes; want a bounded streaming copy", allocated)
	}
	if stagingFileHash(t, source) != stagingFileHash(t, target) {
		t.Fatal("staged artifact differs from source")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("staged permissions=%v, want 0700", info.Mode().Perm())
	}
	file, err = os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteAt([]byte("changed"), 0)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("mutate staged artifact: %v; close: %v", err, closeErr)
	}
	if stagingFileHash(t, source) == stagingFileHash(t, target) {
		t.Fatal("staged artifact mutation changed the shared source")
	}
}

func stagingFileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}
