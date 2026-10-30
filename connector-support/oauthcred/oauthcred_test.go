package oauthcred_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// 1. Atomic save + load roundtrip
func TestFileStore_AtomicSaveLoadRoundtrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := oauthcred.NewFileStore(path)

	now := time.Now().Truncate(time.Second)
	rec := oauthcred.TokenRecord{
		AccessToken:      "acc-token-123",
		RefreshToken:     "ref-token-456",
		Expiry:           now.Add(1 * time.Hour),
		Quarantined:      false,
		QuarantineReason: "",
	}

	if err := store.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.AccessToken != rec.AccessToken {
		t.Fatalf("access token want %q got %q", rec.AccessToken, loaded.AccessToken)
	}
	if loaded.RefreshToken != rec.RefreshToken {
		t.Fatalf("refresh token want %q got %q", rec.RefreshToken, loaded.RefreshToken)
	}
	if !loaded.Expiry.Equal(rec.Expiry) {
		t.Fatalf("expiry want %v got %v", rec.Expiry, loaded.Expiry)
	}
	if loaded.Quarantined != rec.Quarantined {
		t.Fatalf("quarantined want %v got %v", rec.Quarantined, loaded.Quarantined)
	}
}

// 2. Permissions 0600 on Unix
func TestFileStore_Permissions_0600(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "perm_token.json")
	store := oauthcred.NewFileStore(path)

	if err := store.Save(oauthcred.TokenRecord{AccessToken: "tok"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Log("skipping 0600 mode bit check on Windows (no meaningful Unix mode bits; mirroring Codex)")
		// Verify Load succeeds on Windows
		if _, err := store.Load(); err != nil {
			t.Fatalf("load on windows: %v", err)
		}
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 permissions, got %o", perm)
	}

	// Create a file with group/other readable mode
	badPath := filepath.Join(dir, "bad_perm.json")
	if err := os.WriteFile(badPath, []byte(`{"access_token":"bad"}`), 0o644); err != nil {
		t.Fatalf("write bad file: %v", err)
	}
	badStore := oauthcred.NewFileStore(badPath)
	_, err = badStore.Load()
	if err == nil {
		t.Fatal("expected load of 0644 file to fail on Unix, got nil")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// 3. Redaction: errors/strings from helpers never contain the token
func TestRedaction_NeverLeaksSecrets(t *testing.T) {
	t.Parallel()

	secretAccess := "super-secret-access-token-999"
	secretRefresh := "super-secret-refresh-token-888"

	t.Run("Redact helper", func(t *testing.T) {
		t.Parallel()
		raw := fmt.Sprintf("failed to exchange: %s and %s", secretAccess, secretRefresh)
		clean := oauthcred.Redact(raw, secretAccess, secretRefresh)
		if strings.Contains(clean, secretAccess) {
			t.Fatalf("redacted string still contains secretAccess: %s", clean)
		}
		if strings.Contains(clean, secretRefresh) {
			t.Fatalf("redacted string still contains secretRefresh: %s", clean)
		}
		if !strings.Contains(clean, "[REDACTED]") {
			t.Fatalf("expected [REDACTED] placeholder in: %s", clean)
		}
	})

	t.Run("RedactError helper", func(t *testing.T) {
		t.Parallel()
		rawErr := fmt.Errorf("network failure during token exchange for %s", secretRefresh)
		cleanErr := oauthcred.RedactError(rawErr, secretRefresh)
		if strings.Contains(cleanErr.Error(), secretRefresh) {
			t.Fatalf("redacted error contains secret: %s", cleanErr.Error())
		}
	})

	t.Run("Session refresh error redacts tokens", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		store := oauthcred.NewFileStore(filepath.Join(dir, "tokens.json"))
		_ = store.Save(oauthcred.TokenRecord{
			AccessToken:  secretAccess,
			RefreshToken: secretRefresh,
			Expiry:       time.Now().Add(-1 * time.Minute),
		})

		refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
			return "", "", time.Time{}, fmt.Errorf("provider returned invalid_grant for token %s", rt)
		})

		sess := oauthcred.NewSession(store, refresher)
		_, err := sess.Token(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if strings.Contains(err.Error(), secretRefresh) {
			t.Fatalf("session token error leaked refresh token: %s", err.Error())
		}

		rec, err := sess.Record()
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if strings.Contains(rec.QuarantineReason, secretRefresh) {
			t.Fatalf("quarantine reason leaked refresh token: %s", rec.QuarantineReason)
		}
	})
}

// 4. PKCE challenge is S256 of verifier; state validation fail-closed
func TestPKCE_S256_AndStateValidation(t *testing.T) {
	t.Parallel()

	verifier, challenge, state, err := oauthcred.GeneratePKCE()
	if err != nil {
		t.Fatalf("generate pkce: %v", err)
	}

	if len(verifier) < 43 {
		t.Fatalf("verifier too short: %d (want >= 43)", len(verifier))
	}
	if challenge == "" || state == "" {
		t.Fatal("challenge or state is empty")
	}

	// Verify challenge == base64url(SHA256(verifier))
	h := sha256.Sum256([]byte(verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(h[:])
	if challenge != expectedChallenge {
		t.Fatalf("challenge mismatch: want %q got %q", expectedChallenge, challenge)
	}

	computed := oauthcred.ComputePKCEChallenge(verifier)
	if computed != expectedChallenge {
		t.Fatalf("compute challenge mismatch: want %q got %q", expectedChallenge, computed)
	}

	// ValidateState tests
	if err := oauthcred.ValidateState(state, state); err != nil {
		t.Fatalf("valid state failed: %v", err)
	}
	if err := oauthcred.ValidateState(state, "tampered-state"); err == nil {
		t.Fatal("expected state mismatch error, got nil")
	}
	if err := oauthcred.ValidateState("", state); err == nil {
		t.Fatal("expected empty state error, got nil")
	}
	if err := oauthcred.ValidateState(state, ""); err == nil {
		t.Fatal("expected empty state error, got nil")
	}
	if err := oauthcred.ValidateState("", ""); err == nil {
		t.Fatal("expected empty state error, got nil")
	}
}

func TestValidateState_TimingSafeSemantics(t *testing.T) {
	t.Parallel()

	const expected = "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"

	tests := []struct {
		name     string
		expected string
		got      string
		wantErr  bool
	}{
		{name: "exact match", expected: expected, got: expected, wantErr: false},
		{name: "same length differing last char", expected: expected, got: expected[:len(expected)-1] + "X", wantErr: true},
		{name: "same length differing first char", expected: expected, got: "X" + expected[1:], wantErr: true},
		{name: "shorter state", expected: expected, got: expected[:len(expected)/2], wantErr: true},
		{name: "longer state", expected: expected, got: expected + "-extra", wantErr: true},
		{name: "empty expected", expected: "", got: expected, wantErr: true},
		{name: "empty got", expected: expected, got: "", wantErr: true},
		{name: "both empty", expected: "", got: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := oauthcred.ValidateState(tc.expected, tc.got)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

func TestSession_RefreshBeforeExpiry_VsCached(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := oauthcred.NewFileStore(filepath.Join(dir, "tokens.json"))

	var refreshCalls atomic.Int32
	refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
		refreshCalls.Add(1)
		return "new-access-token", "new-refresh-token", time.Now().Add(2 * time.Hour), nil
	})

	sess := oauthcred.NewSession(store, refresher, oauthcred.WithSkew(60*time.Second))

	// Case 1: Fresh token (expires in 2 hours) -> refresher NOT called
	_ = store.Save(oauthcred.TokenRecord{
		AccessToken:  "cached-access-token",
		RefreshToken: "refresh-token-1",
		Expiry:       time.Now().Add(2 * time.Hour),
	})

	tok, err := sess.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok != "cached-access-token" {
		t.Fatalf("expected cached token, got %q", tok)
	}
	if refreshCalls.Load() != 0 {
		t.Fatalf("expected 0 refresh calls, got %d", refreshCalls.Load())
	}

	// Case 2: Expiring token within 60s skew (expires in 30 seconds) -> refresher called
	_ = store.Save(oauthcred.TokenRecord{
		AccessToken:  "about-to-expire",
		RefreshToken: "refresh-token-2",
		Expiry:       time.Now().Add(30 * time.Second),
	})

	tok, err = sess.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok != "new-access-token" {
		t.Fatalf("expected new-access-token, got %q", tok)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("expected 1 refresh call, got %d", refreshCalls.Load())
	}

	// Verify persistence of new tokens
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.AccessToken != "new-access-token" || rec.RefreshToken != "new-refresh-token" {
		t.Fatalf("unexpected persisted tokens: %+v", rec)
	}
}

// 6. invalid_grant quarantines; second Token does not replay Refresher
func TestSession_InvalidGrant_Quarantines_NoReplay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := oauthcred.NewFileStore(filepath.Join(dir, "tokens.json"))

	_ = store.Save(oauthcred.TokenRecord{
		AccessToken:  "expired-tok",
		RefreshToken: "ref-tok",
		Expiry:       time.Now().Add(-10 * time.Minute),
	})

	var refreshCalls atomic.Int32
	refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
		refreshCalls.Add(1)
		return "", "", time.Time{}, errors.New("oauth: server returned invalid_grant: token revoked")
	})

	sess := oauthcred.NewSession(store, refresher)

	// First call: triggers refresher, gets invalid_grant, quarantines
	_, err := sess.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, oauthcred.ErrTerminalRefresh) {
		t.Fatalf("expected ErrTerminalRefresh in %v", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", refreshCalls.Load())
	}

	// Check store record is quarantined
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !rec.Quarantined {
		t.Fatal("expected record to be marked quarantined")
	}
	if !strings.Contains(rec.QuarantineReason, "invalid_grant") {
		t.Fatalf("unexpected reason: %s", rec.QuarantineReason)
	}

	// Second call: must immediately fail with ErrQuarantined WITHOUT calling refresher again
	_, err = sess.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("expected ErrQuarantined in %v", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresher replayed! expected 1 call, got %d", refreshCalls.Load())
	}
}

// 7. HTTP 401/403 terminal quarantine (if you classify those as terminal)
func TestSession_HTTPTerminalStatus_Quarantines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		terminal   bool
		callCount2 int32
	}{
		{
			name:       "HTTP 401 Unauthorized is terminal",
			err:        &oauthcred.HTTPError{StatusCode: 401, Body: "Unauthorized"},
			terminal:   true,
			callCount2: 1, // no replay
		},
		{
			name:       "HTTP 403 Forbidden is terminal",
			err:        &oauthcred.HTTPError{StatusCode: 403, Body: "Forbidden"},
			terminal:   true,
			callCount2: 1, // no replay
		},
		{
			name:       "HTTP 429 Too Many Requests is transient (not terminal)",
			err:        &oauthcred.HTTPError{StatusCode: 429, Body: "Too many requests"},
			terminal:   false,
			callCount2: 2, // will replay on next attempt
		},
		{
			name:       "HTTP 408 Request Timeout is transient (not terminal)",
			err:        &oauthcred.HTTPError{StatusCode: 408, Body: "Request timeout"},
			terminal:   false,
			callCount2: 2, // will replay on next attempt
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store := oauthcred.NewFileStore(filepath.Join(dir, "tokens.json"))
			_ = store.Save(oauthcred.TokenRecord{
				AccessToken:  "expired",
				RefreshToken: "ref",
				Expiry:       time.Now().Add(-5 * time.Minute),
			})

			var calls atomic.Int32
			refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
				calls.Add(1)
				return "", "", time.Time{}, tc.err
			})

			sess := oauthcred.NewSession(store, refresher)

			// Call 1
			_, err := sess.Token(context.Background())
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			rec, _ := store.Load()
			if rec.Quarantined != tc.terminal {
				t.Fatalf("quarantined want %v got %v", tc.terminal, rec.Quarantined)
			}

			// Call 2
			_, _ = sess.Token(context.Background())
			if calls.Load() != tc.callCount2 {
				t.Fatalf("calls want %d got %d", tc.callCount2, calls.Load())
			}
		})
	}
}

// 8. Logout/removal; re-login Save clears quarantine
func TestSession_Logout_AndReLoginClearsQuarantine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := oauthcred.NewFileStore(filepath.Join(dir, "tokens.json"))

	// 1. Quarantined session
	_ = store.Save(oauthcred.TokenRecord{
		AccessToken:      "bad",
		Quarantined:      true,
		QuarantineReason: "manual test quarantine",
	})

	refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
		return "", "", time.Time{}, errors.New("should not be called")
	})
	sess := oauthcred.NewSession(store, refresher)

	_, err := sess.Token(context.Background())
	if !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("expected ErrQuarantined, got %v", err)
	}

	// 2. Logout removes file
	if err := sess.Logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}

	_, err = sess.Token(context.Background())
	if !errors.Is(err, oauthcred.ErrLoggedOut) {
		t.Fatalf("expected ErrLoggedOut after logout, got %v", err)
	}

	// 3. Re-login Save clears quarantine
	newRec := oauthcred.TokenRecord{
		AccessToken:      "clean-new-token",
		RefreshToken:     "new-refresh-token",
		Expiry:           time.Now().Add(2 * time.Hour),
		Quarantined:      false,
		QuarantineReason: "",
	}
	if err := sess.Save(newRec); err != nil {
		t.Fatalf("save: %v", err)
	}

	tok, err := sess.Token(context.Background())
	if err != nil {
		t.Fatalf("token after re-login: %v", err)
	}
	if tok != "clean-new-token" {
		t.Fatalf("expected clean-new-token, got %q", tok)
	}
}

// F5 regression: terminal refresh quarantine must latch in-process even when
// quarantine persistence fails. Refresher must be called exactly once across
// repeated Token() calls until explicit re-login/reset, and the Save failure
// must be surfaced to the caller (not silently discarded).
func TestSession_TerminalQuarantine_PersistFailure_NoReplay(t *testing.T) {
	t.Parallel()

	errSave := errors.New("quarantine persist boom")

	stub := &quarantineFailStore{
		rec: oauthcred.TokenRecord{
			AccessToken:  "expired-tok",
			RefreshToken: "ref-tok",
			Expiry:       time.Now().Add(-10 * time.Minute),
		},
		failQuarantineSave: true,
		saveErr:            errSave,
	}

	var refreshCalls atomic.Int32
	refresher := oauthcred.RefresherFunc(func(ctx context.Context, rt string) (string, string, time.Time, error) {
		refreshCalls.Add(1)
		return "", "", time.Time{}, errors.New("oauth: server returned invalid_grant: token revoked")
	})

	sess := oauthcred.NewSession(stub, refresher)

	// First call: terminal failure; Save fails so error must surface the failure.
	_, err := sess.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, oauthcred.ErrTerminalRefresh) {
		t.Fatalf("expected ErrTerminalRefresh in %v", err)
	}
	if !errors.Is(err, errSave) {
		t.Fatalf("expected Save error %v surfaced in %v", errSave, err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected 1 refresh call, got %d", got)
	}
	if stub.saveCalls != 1 {
		t.Fatalf("expected 1 save attempt, got %d", stub.saveCalls)
	}

	// Second call: must NOT replay refresher even though persistence failed.
	_, err = sess.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("expected ErrQuarantined in %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresher replayed despite quarantine latch! expected 1 call, got %d", got)
	}

	// Third call: still latched, still no replay.
	_, err = sess.Token(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("expected ErrQuarantined in %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresher replayed despite quarantine latch! expected 1 call, got %d", got)
	}

	// Explicit re-login/reset clears the latch and serves the fresh token.
	fresh := oauthcred.TokenRecord{
		AccessToken:  "clean-new-token",
		RefreshToken: "new-refresh-token",
		Expiry:       time.Now().Add(2 * time.Hour),
	}
	if err := sess.Save(fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}
	tok, err := sess.Token(context.Background())
	if err != nil {
		t.Fatalf("token after re-login: %v", err)
	}
	if tok != "clean-new-token" {
		t.Fatalf("expected clean-new-token, got %q", tok)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected no additional refresh after re-login with fresh token, got %d calls", got)
	}
}

// quarantineFailStore fails quarantine persistence while allowing plain
// credential writes (re-login) to succeed, modelling a transient disk failure.
type quarantineFailStore struct {
	rec                oauthcred.TokenRecord
	failQuarantineSave bool
	saveErr            error
	saveCalls          int
}

func (s *quarantineFailStore) Load() (oauthcred.TokenRecord, error) { return s.rec, nil }

func (s *quarantineFailStore) Save(rec oauthcred.TokenRecord) error {
	s.saveCalls++
	if rec.Quarantined && s.failQuarantineSave {
		return s.saveErr
	}
	s.rec = rec
	return nil
}

func (s *quarantineFailStore) Delete() error {
	s.rec = oauthcred.TokenRecord{}
	return nil
}

func (s *quarantineFailStore) Path() string { return "quarantine-fail-store" }

// 9. No import of pkg/aipapi OAuth types (there must be none added)
func TestHygiene_NoAipapiOAuthTypes(t *testing.T) {
	t.Parallel()

	// 1. Check oauthcred go.mod has no dependencies
	modBytes, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if strings.Contains(string(modBytes), "require") {
		t.Fatalf("connector-support/oauthcred must be stdlib only, got requires:\n%s", string(modBytes))
	}

	// 2. Check pkg/aipapi directory for absence of OAuth types
	aipapiDir := filepath.Join("..", "..", "pkg", "aipapi")
	entries, err := os.ReadDir(aipapiDir)
	if err != nil {
		t.Fatalf("read aipapi dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(aipapiDir, e.Name()))
		if err != nil {
			t.Fatalf("read file %s: %v", e.Name(), err)
		}
		s := string(content)
		if strings.Contains(s, "TokenRecord") || strings.Contains(s, "GeneratePKCE") || strings.Contains(s, "Refresher") {
			t.Fatalf("pkg/aipapi must not contain OAuth types! Found in %s", e.Name())
		}
	}
}
