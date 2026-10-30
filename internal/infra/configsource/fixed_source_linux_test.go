//go:build linux

package configsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixedSource_UnsupportedLinuxBootstrapHasUnavailableBaseline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: {address: 127.0.0.1:0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	source.leaseDetector = func(*os.File) (sourceLeaseEvidence, bool) { return sourceLeaseEvidence{}, false }
	snapshot, result, err := source.ReadStable(context.Background(), nil)
	if err != nil || result != AtomicEligible || len(snapshot.Bytes) == 0 {
		t.Fatalf("unsupported-filesystem bootstrap must remain bounded and valid: result=%q err=%v", result, err)
	}
	active, owner := snapshot.TakeBaseline()
	defer func() { _ = owner.Close(context.Background()) }()
	if !active.leaseRequired || !active.leaseUnavailable {
		t.Fatalf("Linux fallback baseline must explicitly mark lease unavailable: %+v", active)
	}
	if _, _, err := source.ReadStable(context.Background(), active); err == nil {
		t.Fatal("runtime comparison without a matching ext4 lease must fail closed")
	} else if category, ok := CategoryOf(err); !ok || category != CategoryNonAtomicUpdate {
		t.Fatalf("runtime comparison category=%q ok=%v err=%v", category, ok, err)
	}
}

func TestFixedSource_TargetCloseFailureIsSourceIntegrity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: {address: 127.0.0.1:0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	source.leaseDetector = func(*os.File) (sourceLeaseEvidence, bool) { return sourceLeaseEvidence{}, false }
	closeFailure := errors.New("raw target close detail")
	var closeCalls int
	source.closeFile = func(file *os.File) error {
		closeCalls++
		if err := file.Close(); err != nil {
			return err
		}
		if closeCalls == 1 {
			return closeFailure
		}
		return nil
	}

	_, _, err = source.ReadStable(context.Background(), nil)
	if err == nil {
		t.Fatal("target close failure must fail source loading")
	}
	if category, ok := CategoryOf(err); !ok || category != CategoryPartialUnreadable {
		t.Fatalf("target close failure category=%q ok=%v err=%v", category, ok, err)
	}
	if !IsSourceCleanupError(err) {
		t.Fatalf("target close failure must retain cleanup marker: %v", err)
	}
	if !errors.Is(err, closeFailure) {
		t.Fatalf("target close cause must remain discoverable: %v", err)
	}
	if strings.Contains(err.Error(), closeFailure.Error()) {
		t.Fatalf("raw close error escaped safe source error: %v", err)
	}
	if closeCalls != 2 {
		t.Fatalf("target and candidate close calls=%d want 2", closeCalls)
	}
}

func TestFixedSource_EarlyCandidateCloseFailureKeepsPrimaryCategory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	source.leaseDetector = func(*os.File) (sourceLeaseEvidence, bool) { return sourceLeaseEvidence{}, false }
	closeFailure := errors.New("raw early candidate close detail")
	var closeCalls int
	source.closeFile = func(file *os.File) error {
		closeCalls++
		if err := file.Close(); err != nil {
			return err
		}
		if closeCalls == 2 {
			return closeFailure
		}
		return nil
	}
	_, _, err = source.ReadStable(context.Background(), nil)
	if category, ok := CategoryOf(err); !ok || category != CategoryEmpty {
		t.Fatalf("candidate close failure replaced primary category: category=%q ok=%v err=%v", category, ok, err)
	}
	if !IsSourceCleanupError(err) || !errors.Is(err, closeFailure) {
		t.Fatalf("early candidate close failure marker/cause lost: %v", err)
	}
	if strings.Contains(err.Error(), closeFailure.Error()) {
		t.Fatalf("raw early close error escaped: %v", err)
	}
	if closeCalls != 2 {
		t.Fatalf("target and candidate close calls=%d want 2", closeCalls)
	}
}

func TestFixedSource_ClassifierRejectionCloseFailureRetainsMarker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("accepted: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := source.ReadStable(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	active, owner := initial.TakeBaseline()
	defer func() { _ = owner.Close(context.Background()) }()
	if active.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		t.Skip("test filesystem does not provide supported ext4 lease evidence")
	}
	if err := os.WriteFile(path, []byte("accepted: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("raw rejected candidate close detail")
	var closeCalls int
	source.closeFile = func(file *os.File) error {
		closeCalls++
		if err := file.Close(); err != nil {
			return err
		}
		if closeCalls == 2 {
			return closeFailure
		}
		return nil
	}
	_, result, err := source.ReadStable(context.Background(), active)
	if result != AtomicReject || err == nil {
		t.Fatalf("in-place candidate result=%q err=%v, want rejected update", result, err)
	}
	if category, ok := CategoryOf(err); !ok || category != CategoryNonAtomicUpdate {
		t.Fatalf("cleanup replaced classifier rejection: category=%q ok=%v err=%v", category, ok, err)
	}
	if !IsSourceCleanupError(err) || !errors.Is(err, closeFailure) {
		t.Fatalf("classifier-rejection cleanup marker/cause lost: %v", err)
	}
	if strings.Contains(err.Error(), closeFailure.Error()) {
		t.Fatalf("raw rejected candidate close error escaped: %v", err)
	}
	if closeCalls != 2 {
		t.Fatalf("target and accepted candidate close calls=%d want 2", closeCalls)
	}
}
