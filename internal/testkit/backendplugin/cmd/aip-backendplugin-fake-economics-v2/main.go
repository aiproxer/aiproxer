// Command aip-backendplugin-fake-economics-v2 is the test capability variant of
// the deterministic fake backendplugin that advertises the host-bound
// economic evidence contract. On a successful execute it emits typed V2
// provider claims whose identity and quantity facts come from the explicit
// fixture source the instance configuration supplies.
//
// It lets an integration test drive the real executable-connector path
// (discovered connector -> host backend -> runtime commit -> durable journal
// and sealed B-leg) with a provider claim that carries a provider account key
// and the provider's reported quantities, and no money value at all.
//
// The connector also carries a test-only protocol offer switch. It selects
// whether the connector advertises the host-bound economic evidence contract
// or behaves as a minor-9 peer that negotiated the accounting evidence
// contract but never the host-bound one. The switch is consumed before the
// negotiation, when the connector's own advertised offer is built, and it
// never changes the host's offer, the generic fake's default or any
// production behaviour.
//
// The switch travels with the connector's own build (the host stages the
// executable under a digest name and launches it without arguments and with a
// minimal environment), and the command line and the environment can override
// it for a connector that is run directly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	// peerOfferModeFlag selects the connector's advertised protocol offer.
	peerOfferModeFlag = "peer-offer"
	// peerOfferDescribeFlag asks the connector to print its advertised offer
	// and exit, so a proof can read the child's real descriptor.
	peerOfferDescribeFlag = "describe"
	// peerOfferModeEnv is the environment carrier of the same switch.
	peerOfferModeEnv = "AIP_FAKE_PEER_OFFER"
	// peerOfferMinor9 is the minor-9 peer offer.
	peerOfferMinor9 = "minor9"
)

// peerOffer is the connector's build-time protocol offer. A staged peer is
// built with "-ldflags=-X main.peerOffer=minor9" when a proof needs a peer
// that never opted into the host-bound contract: the host launches a
// discovered connector without arguments and with a minimal environment, and
// it stages the executable under a digest name, so the offer switch has to
// travel with the connector's own build. The empty value is the connector's
// own default offer.
var peerOffer = ""

func main() {
	flagMode, describe := peerOfferFlags()
	if describe {
		if err := describePeerOffer(flagMode); err != nil {
			fmt.Fprintf(os.Stderr, "aip-backendplugin-fake-economics-v2: %v\n", err)
			os.Exit(1)
		}
		return
	}
	backendplugin.Main("aip-backendplugin-fake-economics-v2", &peerService{
		FakeService: &fakebp.FakeService{
			Mode:             fakebp.ModeValid,
			EconomicsV2:      true,
			CanonicalModelID: "fake/fake-model",
			NativeModelID:    "fake-model",
		},
		mode: peerOfferMode(flagMode),
	})
}

// peerOfferFlags reads the connector's own test-only flags. They are removed
// from the argument list, so the SDK's own flag parsing is unaffected.
func peerOfferFlags() (mode string, describe bool) {
	fs := flag.NewFlagSet("peer-offer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	modeFlag := fs.String(peerOfferModeFlag, "", "advertised protocol offer of this connector")
	describeFlag := fs.Bool(peerOfferDescribeFlag, false, "print the advertised protocol offer and exit")
	// A flag this set does not know belongs to the SDK's own set: the parse
	// stops there and the defaults stay in place.
	_ = fs.Parse(os.Args[1:])
	os.Args = append([]string{os.Args[0]}, filterPeerOfferArgs(os.Args[1:])...)
	return strings.TrimSpace(*modeFlag), *describeFlag
}

// filterPeerOfferArgs removes the connector's own flags from the argument list.
func filterPeerOfferArgs(args []string) []string {
	kept := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-"+peerOfferDescribeFlag || strings.HasPrefix(arg, "-"+peerOfferModeFlag+"=") {
			continue
		}
		kept = append(kept, arg)
	}
	return kept
}

// peerOfferMode reports the connector's advertised protocol offer. The CLI
// flag wins, then the environment, then the connector's build-time offer.
func peerOfferMode(flagValue string) string {
	if mode := strings.TrimSpace(flagValue); mode != "" {
		return mode
	}
	if mode := strings.TrimSpace(os.Getenv(peerOfferModeEnv)); mode != "" {
		return mode
	}
	return strings.TrimSpace(peerOffer)
}

// peerOfferDescription is the connector's advertised offer, as a proof reads
// it back from the child.
type peerOfferDescription struct {
	ProtocolMinor uint32   `json:"protocol_minor"`
	Features      []string `json:"features"`
}

// describePeerOffer prints the connector's advertised offer and returns.
func describePeerOffer(flagValue string) error {
	desc, err := (&peerService{
		FakeService: &fakebp.FakeService{
			Mode:             fakebp.ModeValid,
			EconomicsV2:      true,
			CanonicalModelID: "fake/fake-model",
			NativeModelID:    "fake-model",
		},
		mode: peerOfferMode(flagValue),
	}).Describe(context.Background())
	if err != nil {
		return err
	}
	features := make([]string, 0, len(desc.Features))
	for _, feature := range desc.Features {
		features = append(features, feature.Name)
	}
	encoded, err := json.Marshal(peerOfferDescription{ProtocolMinor: desc.ProtocolMinor, Features: features})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

// peerService is the staged connector's service with the test-only offer mode
// applied to its advertised descriptor.
type peerService struct {
	*fakebp.FakeService
	mode string
}

// Describe advertises the connector's protocol offer.
//
// The default is the connector's own offer: protocol minor 10 with the
// host-bound economic evidence feature. The minor-9 mode advertises a peer
// that negotiated the accounting evidence contract but never the host-bound
// one, so the negotiated feature set cannot carry the host-bound contract and
// the host never rewrites the peer's claim.
func (s *peerService) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	desc, err := s.FakeService.Describe(ctx)
	if err != nil {
		return desc, err
	}
	if s.mode != peerOfferMinor9 {
		return desc, nil
	}
	desc.ProtocolMinor = backendplugin.ProtocolMinorAccountingEvidenceV2
	features := make([]backendplugin.Feature, 0, len(desc.Features))
	for _, feature := range desc.Features {
		if feature.Name == backendplugin.FeatureHostBoundEconomicEvidenceV2 {
			continue
		}
		features = append(features, feature)
	}
	desc.Features = features
	return desc, nil
}
