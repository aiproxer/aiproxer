package product

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ErrUnknownModel is returned when a route model is not a known Cursor native
// id or mapped canonical identity from the discovered inventory index.
var ErrUnknownModel = errors.New("cursorcliacp: unknown model")

const defaultConfiguredModel = "composer-2"

func cursorCanonicalFallback(native string) string {
	return canonicalIDForNative(native)
}

// canonicalIDForNative builds CanonicalID as cursor/<normalized>, stripping a
// leading "cursor-" from the native CLI id for the canonical path only.
// NativeID itself is left unchanged by callers.
func canonicalIDForNative(native string) string {
	path := strings.TrimSpace(native)
	if after, ok := strings.CutPrefix(path, "cursor-"); ok {
		path = after
	}
	return vendorPrefix + "/" + path
}

func modelsFromListing(ids []string) []modelinventory.Model {
	models := make([]modelinventory.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, modelinventory.Model{
			CanonicalID: canonicalIDForNative(id),
			NativeID:    id,
			DisplayName: id,
		})
	}
	return models
}

func (s *cursorSpec) resolveNativeModel(effective string) (string, error) {
	if s == nil {
		return "", ErrUnknownModel
	}
	identity := strings.TrimSpace(acp.ResolveVendorModel(vendorPrefix, s.cfg.Model, defaultConfiguredModel, effective))
	if identity == "" {
		return "", ErrUnknownModel
	}
	if s.index.IsKnownNative(identity) {
		return identity, nil
	}
	// Treat identity as a canonical path segment (after vendor strip) or a full
	// CanonicalID when the caller already included the cursor/ prefix form.
	prefix := s.prefixSlash
	if prefix == "" {
		prefix = vendorPrefix + "/"
	}
	if native, ok := s.index.NativeForCanonical(prefix + identity); ok {
		return native, nil
	}
	if native, ok := s.index.NativeForCanonical(identity); ok {
		return native, nil
	}
	return "", ErrUnknownModel
}

type modelsProvider struct {
	binary   string
	endpoint string
	run      func(ctx context.Context, binary, endpoint string) ([]byte, error)
}

func newModelsProvider(binary, endpoint string) modelinventory.Provider {
	return modelsProvider{binary: binary, endpoint: endpoint, run: defaultModelsCommandRunner}
}

// listModelsArgs builds `agent [-e <endpoint>] --list-models` argv (no binary).
func listModelsArgs(endpoint string) []string {
	args := make([]string, 0, 3)
	if ep := strings.TrimSpace(endpoint); ep != "" {
		args = append(args, "-e", ep)
	}
	return append(args, "--list-models")
}

// defaultModelsCommandRunner runs `agent [-e <endpoint>] --list-models` with
// no shell. Stderr is folded into the returned error text for OperationalError.Err.
func defaultModelsCommandRunner(ctx context.Context, binary, endpoint string) ([]byte, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	cmd := exec.CommandContext(ctx, binary, listModelsArgs(endpoint)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, acp.SanitizeBoundStderr(stderr.Bytes()))
		}
		return nil, err
	}
	return out, nil
}

func (p modelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	run := p.run
	if run == nil {
		run = defaultModelsCommandRunner
	}
	out, err := run(ctx, p.binary, p.endpoint)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return modelinventory.Snapshot{}, err
		}
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  fmt.Errorf("agent --list-models: %w", err),
		}
	}
	models := modelsFromListing(parseAgentModelsListing(string(out)))
	if models == nil {
		models = []modelinventory.Model{}
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now().UTC(),
		Models:   models,
		Warnings: []string{},
	}, nil
}
