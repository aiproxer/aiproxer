package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/diagredact"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// DoctorTarget is one explicitly selected configured plugin instance.
type DoctorTarget struct {
	InstanceID string
	Kind       string
	Artifact   *trust.VerifiedArtifact
	ConfigYAML []byte
	Secrets    backendplugin.SecretBundle
	Model      processhost.ProcessModel
}

// DoctorResult is a bounded per-instance doctor outcome (no secrets/full paths).
type DoctorResult struct {
	InstanceID string        `json:"instance_id"`
	Kind       string        `json:"kind,omitempty"`
	State      catalog.State `json:"state"`
	Reason     string        `json:"reason"`
	Guidance   string        `json:"guidance,omitempty"`
	Launched   bool          `json:"launched"`
}

// DoctorReport aggregates selected-instance doctor results.
type DoctorReport struct {
	Results []DoctorResult `json:"results"`
}

// DoctorInput runs Activate only for explicit InstanceIDs.
type DoctorInput struct {
	InstanceIDs []string
	Targets     map[string]DoctorTarget
	Host        *processhost.Host
	// DialAndConfigure defaults to a no-op success after peer auth (channel check).
	DialAndConfigure func(ctx context.Context, conn net.Conn, peer processhost.PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error
}

// Doctor launches only the selected configured instance IDs.
// It never walks discovered plugins and never delivers secrets when peer/channel auth fails
// (processhost.Activate withholds DialAndConfigure until peer success).
func Doctor(ctx context.Context, in DoctorInput) (DoctorReport, error) {
	if ctx == nil {
		return DoctorReport{}, fmt.Errorf("diagnostics/doctor: nil context")
	}
	if in.Host == nil {
		return DoctorReport{}, fmt.Errorf("diagnostics/doctor: nil host")
	}
	if len(in.InstanceIDs) == 0 {
		return DoctorReport{}, fmt.Errorf("diagnostics/doctor: at least one instance id required")
	}
	dial := in.DialAndConfigure
	if dial == nil {
		dial = func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		}
	}

	var results []DoctorResult
	for _, id := range in.InstanceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			results = append(results, DoctorResult{
				State:    catalog.StateFailed,
				Reason:   "empty_instance_id",
				Guidance: "pass an explicit configured backend instance id",
			})
			continue
		}
		target, ok := in.Targets[id]
		if !ok {
			results = append(results, DoctorResult{
				InstanceID: id,
				State:      catalog.StateFailed,
				Reason:     string(catalog.ReasonEnabledMissing),
				Guidance:   "instance is not an enabled configured backend; doctor never launches discovered-only plugins",
			})
			continue
		}
		if target.Artifact == nil {
			results = append(results, DoctorResult{
				InstanceID: id,
				Kind:       target.Kind,
				State:      catalog.StateFailed,
				Reason:     string(catalog.ReasonEnabledMissing),
				Guidance:   "configured plugin artifact is missing or failed trust verification; install a trusted manifest and matching executable",
			})
			continue
		}
		model := target.Model
		if model == "" {
			model = processhost.ProcessModelPerInstance
		}
		_, err := in.Host.Activate(ctx, processhost.ActivateRequest{
			InstanceID:       id,
			Artifact:         target.Artifact,
			Model:            model,
			FactoryKind:      target.Kind,
			ConfigYAML:       target.ConfigYAML,
			Secrets:          target.Secrets,
			DialAndConfigure: dial,
		})
		if err != nil {
			reason, guidance := classifyDoctorErr(err)
			results = append(results, DoctorResult{
				InstanceID: id,
				Kind:       target.Kind,
				State:      catalog.StateFailed,
				Reason:     reason,
				Guidance:   guidance,
				Launched:   true,
			})
			continue
		}
		results = append(results, DoctorResult{
			InstanceID: id,
			Kind:       target.Kind,
			State:      catalog.StateActive,
			Reason:     string(processhost.ReasonOK),
			Guidance:   "secure peer-authenticated channel established",
			Launched:   true,
		})
		_ = in.Host.CloseInstance(id)
	}
	return DoctorReport{Results: results}, nil
}

func classifyDoctorErr(err error) (reason, guidance string) {
	switch {
	case errors.Is(err, processhost.ReasonPeerRejected), errors.Is(err, processhost.ReasonPIDReuse), errors.Is(err, processhost.ReasonStaleGeneration):
		return string(processhost.ReasonPeerRejected), "peer authentication failed; credentials were not delivered. verify the plugin process identity and approved local channel profile"
	case errors.Is(err, processhost.ReasonUnsupportedChannel), errors.Is(err, processhost.ReasonLoopbackRejected), errors.Is(err, processhost.ReasonCookieAuthRejected):
		return string(processhost.ReasonUnsupportedChannel), "secure channel establishment failed; credentials were not delivered. use an approved platform IPC profile"
	case errors.Is(err, processhost.ReasonLaunchFailed):
		return string(processhost.ReasonLaunchFailed), "plugin process failed to start; check the trusted executable digest binding and platform support"
	case errors.Is(err, processhost.ReasonHandshakeFailed):
		return string(processhost.ReasonHandshakeFailed), "protocol handshake failed after peer auth; check protocol major/minor compatibility"
	case errors.Is(err, processhost.ReasonArtifactRequired):
		return string(processhost.ReasonArtifactRequired), "verified artifact required before activation"
	default:
		msg := err.Error()
		if len(msg) > 120 {
			msg = msg[:120]
		}
		return "doctor_failed", "plugin doctor check failed: " + sanitizeDoctorMsg(msg)
	}
}

func sanitizeDoctorMsg(msg string) string {
	return diagredact.Sanitize(msg, 120)
}
