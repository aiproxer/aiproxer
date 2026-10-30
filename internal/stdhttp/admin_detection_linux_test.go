//go:build !windows

package stdhttp

import (
	"os"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestDetectRunningAsAdminMatchesTheProcessCredential is the negative half of the
// startup admin guard. The existing table tests substitute a stubbed detector, so
// nothing proves the *real* detector reacts to the process credential. This one
// does: under an administrative euid it must report admin, which is exactly the
// condition `validateStartupSecurity` refuses to start under.
//
// The guard exists because the no-Node host lane runs its verification surface
// with a non-administrative euid: if this detector ever stopped reporting an
// administrative caller, that lane would silently start measuring a privileged
// surface instead.
func TestDetectRunningAsAdminMatchesTheProcessCredential(t *testing.T) {
	admin, err := detectRunningAsAdmin()
	if err != nil {
		t.Fatalf("detectRunningAsAdmin: %v", err)
	}
	if want := os.Geteuid() == 0; admin != want {
		t.Fatalf("detectRunningAsAdmin() = %t for euid %d, want %t", admin, os.Geteuid(), want)
	}
	if admin {
		cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:8080"}}
		if err := validateStartupSecurity(cfg); err == nil {
			t.Fatal("an administrative caller must be refused at startup")
		}
	}
}
