package contract

import (
	"context"
	"net/netip"
	"testing"
)

func TestWithSourceAddrRoundTripsTheNormalizedAddress(t *testing.T) {
	t.Parallel()

	want := netip.MustParseAddr("198.51.100.10")
	got, ok := SourceAddr(WithSourceAddr(context.Background(), want))
	if !ok {
		t.Fatal("SourceAddr reported no snapshot")
	}
	if got != want {
		t.Fatalf("address = %s, want %s", got, want)
	}
}

func TestSourceAddrNormalizesIPv4MappedIPv6(t *testing.T) {
	t.Parallel()

	got, ok := SourceAddr(WithSourceAddr(context.Background(), netip.MustParseAddr("::ffff:198.51.100.10")))
	if !ok {
		t.Fatal("SourceAddr reported no snapshot")
	}
	if got.Is4In6() || got != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("address = %s (4in6=%v), want normalized 198.51.100.10", got, got.Is4In6())
	}
}

func TestSourceAddrAbsentWithoutGateSnapshot(t *testing.T) {
	t.Parallel()

	if addr, ok := SourceAddr(context.Background()); ok || addr.IsValid() {
		t.Fatalf("address = %s ok=%v, want no snapshot", addr, ok)
	}
	if addr, ok := SourceAddr(nil); ok || addr.IsValid() { //nolint:staticcheck // deliberate nil ctx; the accessor must tolerate one
		t.Fatalf("nil context: address = %s ok=%v, want no snapshot", addr, ok)
	}
}

func TestWithSourceAddrIgnoresInvalidAddressAndPreservesParent(t *testing.T) {
	t.Parallel()

	parent := WithSourceAddr(context.Background(), netip.MustParseAddr("198.51.100.10"))
	got, ok := SourceAddr(WithSourceAddr(parent, netip.Addr{}))
	if !ok {
		t.Fatal("invalid address must not erase an existing snapshot")
	}
	if got != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("address = %s, want the preserved snapshot", got)
	}
}
