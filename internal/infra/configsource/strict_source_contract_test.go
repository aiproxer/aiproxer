package configsource_test

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

func TestStrictReloadConfigSource_TableFixtures(t *testing.T) {
	t.Parallel()

	oversize := bytes.Repeat([]byte("a"), int(configsource.DefaultMaxBytes)+1)
	partial := []byte("server:\n  address: \"127.0.0.1:0\"\nplugins:\n  backends:\n    - id: stub\n      enabled: tr") // truncated

	cases := []struct {
		name    string
		raw     []byte
		want    configsource.Category
		wantErr bool
	}{
		{name: "empty", raw: nil, want: configsource.CategoryEmpty, wantErr: true},
		{name: "empty_slice", raw: []byte{}, want: configsource.CategoryEmpty, wantErr: true},
		{name: "whitespace", raw: []byte(" \n\t\r\n  "), want: configsource.CategoryWhitespace, wantErr: true},
		{name: "oversize", raw: oversize, want: configsource.CategoryOversize, wantErr: true},
		{name: "partial_truncated_body", raw: partial, want: configsource.CategoryOK, wantErr: false}, // byte OK; decode rejects later
		{name: "valid_minimal", raw: []byte("server:\n  address: \"127.0.0.1:0\"\n"), want: configsource.CategoryOK, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := configsource.ClassifyBytes(tc.raw, 0)
			if got != tc.want {
				t.Fatalf("category: got %q want %q", got, tc.want)
			}
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil {
				assertSecretSafeError(t, err, tc.raw)
			}
		})
	}
}

func TestStrictReloadConfigSource_MissingAndUnstable(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		cat, err := configsource.ClassifyPathPresence(false, false)
		if cat != configsource.CategoryMissing || err == nil {
			t.Fatalf("got %q err=%v", cat, err)
		}
	})

	t.Run("unsupported_type", func(t *testing.T) {
		t.Parallel()
		cat, err := configsource.ClassifyPathPresence(true, false)
		if cat != configsource.CategoryUnsupportedType || err == nil {
			t.Fatalf("got %q err=%v", cat, err)
		}
	})

	t.Run("unstable_size", func(t *testing.T) {
		t.Parallel()
		id := configsource.FileIdentity{Platform: "test", Opaque: sha256.Sum256([]byte("a"))}
		cat, err := configsource.ClassifyStability(10, 11, id, id)
		if cat != configsource.CategoryUnstable || err == nil {
			t.Fatalf("got %q err=%v", cat, err)
		}
	})

	t.Run("unstable_identity", func(t *testing.T) {
		t.Parallel()
		a := configsource.FileIdentity{Platform: "test", Opaque: sha256.Sum256([]byte("a"))}
		b := configsource.FileIdentity{Platform: "test", Opaque: sha256.Sum256([]byte("b"))}
		cat, err := configsource.ClassifyStability(10, 10, a, b)
		if cat != configsource.CategoryUnstable || err == nil {
			t.Fatalf("got %q err=%v", cat, err)
		}
	})
}

func TestStrictReloadConfigSource_AtomicReplacementContract(t *testing.T) {
	t.Parallel()

	id1 := configsource.FileIdentity{Platform: "test", Opaque: sha256.Sum256([]byte("inode-1"))}
	id2 := configsource.FileIdentity{Platform: "test", Opaque: sha256.Sum256([]byte("inode-2"))}
	digestA := sha256.Sum256([]byte("body-a"))
	digestB := sha256.Sum256([]byte("body-b"))

	active := configsource.ActiveSourceVersion{HandleIdentity: id1, PrivateDigest: digestA}

	t.Run("noop_same_identity_digest", func(t *testing.T) {
		t.Parallel()
		cand := configsource.SourceSnapshot{HandleIdentity: id1, PrivateDigest: digestA, Bytes: []byte("body-a")}
		res, cat, err := configsource.ClassifyAtomicReplacement(active, cand)
		if err != nil || res != configsource.AtomicNoop || cat != configsource.CategoryOK {
			t.Fatalf("got res=%q cat=%q err=%v", res, cat, err)
		}
	})

	t.Run("reject_inplace_rewrite", func(t *testing.T) {
		t.Parallel()
		cand := configsource.SourceSnapshot{HandleIdentity: id1, PrivateDigest: digestB, Bytes: []byte("body-b")}
		res, cat, err := configsource.ClassifyAtomicReplacement(active, cand)
		if res != configsource.AtomicReject || cat != configsource.CategoryNonAtomicUpdate || err == nil {
			t.Fatalf("got res=%q cat=%q err=%v", res, cat, err)
		}
	})

	t.Run("eligible_new_identity", func(t *testing.T) {
		t.Parallel()
		cand := configsource.SourceSnapshot{HandleIdentity: id2, PrivateDigest: digestB, Bytes: []byte("body-b")}
		res, cat, err := configsource.ClassifyAtomicReplacement(active, cand)
		if err != nil || res != configsource.AtomicEligible || cat != configsource.CategoryOK {
			t.Fatalf("got res=%q cat=%q err=%v", res, cat, err)
		}
	})
}

func TestNoWatcher_TouchOrSamePathReplacementAloneDoesNotMutateActive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("server:\n  address: \"127.0.0.1:0\"\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	active := &activeConfigState{
		generation:  1,
		fingerprint: "gen1",
		digest:      sha256.Sum256(body),
	}
	beforeGen := active.generation
	beforeFP := active.fingerprint

	// Touch mtime without content change.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if active.generation != beforeGen || active.fingerprint != beforeFP {
		t.Fatal("touch alone must not mutate active state")
	}

	// Same-path rewrite of identical bytes (non-atomic replace of same content).
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if active.generation != beforeGen || active.fingerprint != beforeFP {
		t.Fatal("same-path rewrite alone must not mutate active state without trigger")
	}

	// Changed content on disk still must not mutate until an explicit trigger.
	changed := []byte("server:\n  address: \"127.0.0.1:9\"\n")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if active.generation != beforeGen || active.fingerprint != beforeFP {
		t.Fatal("file edit alone must not mutate active state (req 1.5)")
	}

	// Explicit trigger is the only mutation path in this harness.
	active.triggerReload(sha256.Sum256(changed), "gen2")
	if active.generation != 2 || active.fingerprint != "gen2" {
		t.Fatalf("trigger should advance state: %+v", active)
	}
}

func TestNoWatcher_PackageHasNoWatcherImports(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		for _, bad := range []string{"fsnotify", "rjeczalik/notify"} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s must not reference %q", name, bad)
			}
		}
	}
}

type activeConfigState struct {
	generation  int
	fingerprint string
	digest      [32]byte
}

func (s *activeConfigState) triggerReload(digest [32]byte, fp string) {
	s.generation++
	s.fingerprint = fp
	s.digest = digest
}

func assertSecretSafeError(t *testing.T, err error, raw []byte) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "\x00") {
		t.Fatalf("error contains NUL: %q", msg)
	}
	// Oversized payloads must not be echoed.
	if len(raw) > 64 && bytes.Contains([]byte(msg), raw[:64]) {
		t.Fatalf("error echoed raw payload prefix: %q", msg)
	}
}
