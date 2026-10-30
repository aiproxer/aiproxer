package processhost_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
)

func TestHostSecureProfiles_DarwinFailClosedInventory(t *testing.T) {
	t.Parallel()
	profiles := processhost.HostSecureProfiles()
	if len(profiles) != 3 {
		t.Fatalf("len=%d", len(profiles))
	}
	by := map[processhost.PlatformID]processhost.HostSecureProfile{}
	for _, p := range profiles {
		by[p.Platform] = p
	}
	linux := by[processhost.PlatformLinux]
	if !linux.RuntimeChannelOK || linux.Verification != processhost.PlatformDesignSourceEvidenced {
		t.Fatalf("linux profile=%#v", linux)
	}
	win := by[processhost.PlatformWindows]
	if !win.RuntimeChannelOK || win.Verification != processhost.PlatformDesignSourceEvidenced {
		t.Fatalf("windows profile=%#v", win)
	}
	darwin := by[processhost.PlatformDarwin]
	if darwin.RuntimeChannelOK {
		t.Fatal("darwin must not claim runtime channel support while channel_darwin fails closed")
	}
	if darwin.Verification != processhost.PlatformCompileUnverified {
		t.Fatalf("darwin verification=%q want compile_unverified", darwin.Verification)
	}
	if darwin.RuntimeChannelReason == "" {
		t.Fatal("darwin must record fail-closed reason")
	}
	if processhost.RuntimeChannelSupported(processhost.PlatformDarwin) {
		t.Fatal("RuntimeChannelSupported(darwin) must be false")
	}
	if !processhost.RuntimeChannelSupported(processhost.PlatformLinux) || !processhost.RuntimeChannelSupported(processhost.PlatformWindows) {
		t.Fatal("linux/windows channels must be inventory-supported")
	}
}
