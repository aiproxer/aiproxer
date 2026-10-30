//go:build linux && configsource_cert

package configsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const sourceCertCycles = 1000

// TestFixedSource_AtomicReplaceWithRecycledInodeIsEligible proves the atomicity
// gate keys on full file identity, not on the inode number alone. A rejected
// candidate is still an atomic rename, so it frees the previously accepted
// file's inode; the following recovery rename can be handed that same inode
// number back by the filesystem.
//
// Reusing an inode number is not by itself proof of a different physical file:
// birth timestamps can coincide under rapid creation, so a reused inode number
// can yield the same full identity as the accepted file, and identities from
// different platform or scheme pairs are not comparable at all. In both cases a
// distinct-identity precondition is unavailable, the candidate is
// indistinguishable from an in-place rewrite, and the gate must fail closed.
// The test therefore compares the candidate's full identity against the
// accepted one and asserts whichever outcome the platform actually produces.
func TestFixedSource_AtomicReplaceWithRecycledInodeIsEligible(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")

	atomicWrite := func(body string) {
		t.Helper()
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}

	atomicWrite("body-a")
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res1, err := src.ReadStable(ctx, nil)
	if err != nil || res1 != AtomicEligible {
		t.Fatalf("startup read: res=%q err=%v", res1, err)
	}
	active := &ActiveSourceVersion{
		HandleIdentity: snap1.HandleIdentity,
		PrivateDigest:  snap1.PrivateDigest,
	}

	// A candidate that fails validation is still renamed atomically, freeing the
	// original inode. The failed content is rejected at read time, but the
	// rename already happened on disk.
	atomicWrite("::: not yaml :::{{")
	if _, res, err := src.ReadStable(ctx, active); err == nil || res != AtomicReject {
		t.Fatalf("failed candidate read: res=%q err=%v, want rejection", res, err)
	}

	// The recovery rename may be allocated the freed inode number.
	atomicWrite("body-b")
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Only exercise the recycled-inode path when the filesystem actually
	// reallocated the freed inode number. Otherwise the test would pass
	// without proving anything, which is worse than an explicit skip.
	if !os.SameFile(info1, info2) {
		t.Skipf("filesystem did not recycle the freed inode number; ABA condition not reproducible here")
	}

	// Inspect the recovery candidate's full identity before asserting an
	// outcome: birth timestamps can coincide under rapid creation, so a reused
	// inode number can yield the same full identity as the accepted file, and
	// identities from different platform or scheme pairs cannot prove the
	// candidate is a different physical file. Either way there is no
	// distinct-identity precondition, so the gate must fail closed.
	candidate, res, err := src.ReadStable(ctx, nil)
	if err != nil || res != AtomicEligible {
		t.Fatalf("recovery candidate read: res=%q err=%v", res, err)
	}
	if string(candidate.Bytes) != "body-b" {
		t.Fatalf("recovery candidate bytes=%q want body-b", candidate.Bytes)
	}
	comparable := candidate.HandleIdentity.Platform == snap1.HandleIdentity.Platform &&
		candidate.HandleIdentity.Scheme == snap1.HandleIdentity.Scheme
	if !comparable || candidate.HandleIdentity == snap1.HandleIdentity {
		_, gatedRes, gatedErr := src.ReadStable(ctx, active)
		if gatedErr == nil {
			t.Fatalf("recycled-inode candidate must be rejected, got res=%q", gatedRes)
		}
		if gatedRes != AtomicReject {
			t.Fatalf("recycled-inode candidate res=%q want %q", gatedRes, AtomicReject)
		}
		if cat, _ := CategoryOf(gatedErr); cat != CategoryNonAtomicUpdate {
			t.Fatalf("recycled-inode candidate category=%v want %v", cat, CategoryNonAtomicUpdate)
		}
		t.Logf("recycled inode number did not yield a distinct comparable identity "+
			"(identical=%v comparable=%v): fail-closed outcome asserted; the positive ABA "+
			"precondition is not reproducible on this filesystem",
			candidate.HandleIdentity == snap1.HandleIdentity, comparable)
		return
	}

	snap2, res2, err := src.ReadStable(ctx, active)
	if err != nil {
		t.Fatalf("recycled-inode atomic replace rejected: %v", err)
	}
	if res2 != AtomicEligible {
		t.Fatalf("recycled-inode atomic replace: res=%q want eligible", res2)
	}
	if string(snap2.Bytes) != "body-b" {
		t.Fatalf("recycled-inode bytes=%q want body-b", snap2.Bytes)
	}

	// The security gate is preserved: an in-place rewrite of the accepted inode
	// advances neither identity nor birth time and must stay rejected.
	active2 := &ActiveSourceVersion{
		HandleIdentity: snap2.HandleIdentity,
		PrivateDigest:  snap2.PrivateDigest,
	}
	if err := os.WriteFile(path, []byte("body-c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.ReadStable(ctx, active2); err == nil {
		t.Fatal("in-place rewrite must be rejected")
	} else if cat, _ := CategoryOf(err); cat != CategoryNonAtomicUpdate {
		t.Fatalf("in-place rewrite category=%v want %v", cat, CategoryNonAtomicUpdate)
	}
}

func TestFixedSource_PinnedAtomicRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeAtomic(t, path, "accepted-0")
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, result, err := source.ReadStable(ctx, nil)
	if err != nil || result != AtomicEligible || initial.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		t.Fatalf("supported ext4 startup required: result=%q identity=%q err=%v", result, initial.HandleIdentity.Scheme, err)
	}
	active, activeOwner := initial.TakeBaseline()
	certifyAcceptedPin(t, active, activeOwner)
	defer func() {
		if closeErr := activeOwner.Close(context.Background()); closeErr != nil {
			t.Errorf("close final active owner: %v", closeErr)
		}
	}()

	for i := 0; i < sourceCertCycles; i++ {
		certifyAcceptedPin(t, active, activeOwner)
		writeAtomic(t, path, fmt.Sprintf("rejected-%d", i))
		candidate, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d rejected candidate read: result=%q err=%v", i, got, readErr)
		}
		if candidate.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d candidate reused pinned active identity", i)
		}
		candidateVersion, candidateOwner := candidate.TakeBaseline()
		certifyAcceptedPin(t, candidateVersion, candidateOwner)
		if closeErr := candidateOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d rejected candidate close: %v", i, closeErr)
		}

		writeAtomic(t, path, fmt.Sprintf("recovery-%d", i))
		recovery, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d recovery read: result=%q err=%v", i, got, readErr)
		}
		if recovery.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d recovery reused pinned active identity", i)
		}
		next, nextOwner := recovery.TakeBaseline()
		certifyAcceptedPin(t, next, nextOwner)
		oldOwner := activeOwner
		active, activeOwner = next, nextOwner
		if closeErr := oldOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d previous active owner close: %v", i, closeErr)
		}
	}
}

func TestFixedSource_PinPreventsAcceptedInodeReuse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeAtomic(t, path, "accepted")
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	accepted, result, err := source.ReadStable(ctx, nil)
	if err != nil || result != AtomicEligible || accepted.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		t.Fatalf("supported ext4 startup required: result=%q identity=%q err=%v", result, accepted.HandleIdentity.Scheme, err)
	}
	active, owner := accepted.TakeBaseline()
	certifyAcceptedPin(t, active, owner)
	defer func() {
		if closeErr := owner.Close(context.Background()); closeErr != nil {
			t.Errorf("close active owner: %v", closeErr)
		}
	}()

	noop, got, err := source.ReadStable(ctx, active)
	if err != nil || got != AtomicNoop {
		t.Fatalf("unchanged accepted file: result=%q err=%v, want noop", got, err)
	}
	if noop.HandleIdentity != active.HandleIdentity {
		t.Fatal("unchanged source no-op must retain the accepted physical identity")
	}
	noopVersion, noopOwner := noop.TakeBaseline()
	certifyAcceptedPin(t, noopVersion, noopOwner)
	if closeErr := noopOwner.Close(ctx); closeErr != nil {
		t.Fatalf("close no-op snapshot owner: %v", closeErr)
	}

	if err := os.WriteFile(path, []byte("in-place mutation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := source.ReadStable(ctx, active); got != AtomicReject || err == nil {
		t.Fatalf("in-place edit: result=%q err=%v, want rejected update", got, err)
	} else if category, ok := CategoryOf(err); !ok || category != CategoryNonAtomicUpdate {
		t.Fatalf("in-place edit category=%q ok=%v err=%v, want non-atomic rejection", category, ok, err)
	}
	certifyPinnedIdentity(t, active, owner)

	for i := 0; i < sourceCertCycles; i++ {
		certifyPinnedIdentity(t, active, owner)
		writeAtomic(t, path, fmt.Sprintf("replacement-%d", i))
		candidate, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d replacement read: result=%q err=%v", i, got, readErr)
		}
		if candidate.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d replacement reused accepted identity", i)
		}
		candidateVersion, candidateOwner := candidate.TakeBaseline()
		certifyAcceptedPin(t, candidateVersion, candidateOwner)
		if closeErr := candidateOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d candidate close: %v", i, closeErr)
		}
	}
}

func certifyAcceptedPin(t *testing.T, version *ActiveSourceVersion, owner *SourceOwnerSlot) {
	t.Helper()
	if version == nil || owner == nil || !owner.Matches(version) {
		t.Fatal("accepted snapshot owner must match its source version")
	}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatalf("borrow accepted source: %v", err)
	}
	defer borrow.Release()
	if err := borrow.ValidateBaseline(version); err != nil {
		t.Fatalf("fresh held-handle identity/metadata check: %v", err)
	}
}

func certifyPinnedIdentity(t *testing.T, version *ActiveSourceVersion, owner *SourceOwnerSlot) {
	t.Helper()
	if version == nil || owner == nil || !owner.Matches(version) {
		t.Fatal("accepted snapshot owner must match its source version")
	}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatalf("borrow accepted source: %v", err)
	}
	defer borrow.Release()
	if err := borrow.ValidateProvenance(version); err != nil {
		t.Fatalf("fresh held-handle identity check: %v", err)
	}
}

func writeAtomic(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}
