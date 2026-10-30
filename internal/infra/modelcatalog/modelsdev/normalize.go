package modelsdev

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

// ParseSnapshot decodes a models.dev-style provider map JSON payload, normalizes each model
// to [modelcatalog.ModelFacts], and returns a validated [modelcatalog.Snapshot].
// fetchedAt is stored on the snapshot (proxy-owned acquisition time).
func ParseSnapshot(raw []byte, fetchedAt time.Time) (modelcatalog.Snapshot, error) {
	var zero modelcatalog.Snapshot
	if len(raw) == 0 {
		return zero, errors.New("modelsdev: empty payload")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return zero, fmt.Errorf("modelsdev: decode root: %w", err)
	}
	if root == nil {
		return zero, errors.New("modelsdev: unsupported schema: null root")
	}

	catalog := make(map[string]modelcatalog.ModelFacts)
	for providerKey, providerRaw := range root {
		providerKey = strings.TrimSpace(providerKey)
		if providerKey == "" {
			continue
		}
		var wp wireProvider
		if err := json.Unmarshal(providerRaw, &wp); err != nil {
			return zero, fmt.Errorf("modelsdev: provider %q: %w", providerKey, err)
		}
		if len(wp.Models) == 0 || string(wp.Models) == "null" {
			continue
		}
		var models []wireModel
		if err := json.Unmarshal(wp.Models, &models); err != nil {
			return zero, fmt.Errorf("modelsdev: provider %q models: %w", providerKey, err)
		}
		for _, wm := range models {
			mid := strings.TrimSpace(wm.ID)
			if mid == "" {
				continue
			}
			catalogID := providerKey + "/" + mid
			catalog[catalogID] = normalizeModelFacts(wm)
		}
	}

	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	idx := modelcatalog.NewSnapshotIndex(catalog)
	return modelcatalog.Snapshot{
		Generation:  hash,
		FetchedAt:   fetchedAt,
		ContentHash: hash,
		Index:       idx,
		WirePayload: bytes.Clone(raw),
	}, nil
}

// ParseModelIDs decodes a models.dev-style provider map and returns sorted canonical provider/model IDs.
func ParseModelIDs(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("modelsdev: empty payload")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("modelsdev: decode root: %w", err)
	}
	if root == nil {
		return nil, errors.New("modelsdev: unsupported schema: null root")
	}
	ids := []string{}
	for providerKey, providerRaw := range root {
		providerKey = strings.TrimSpace(providerKey)
		if providerKey == "" {
			continue
		}
		var wp wireProvider
		if err := json.Unmarshal(providerRaw, &wp); err != nil {
			return nil, fmt.Errorf("modelsdev: provider %q: %w", providerKey, err)
		}
		if len(wp.Models) == 0 || string(wp.Models) == "null" {
			continue
		}
		var models []wireModel
		if err := json.Unmarshal(wp.Models, &models); err != nil {
			return nil, fmt.Errorf("modelsdev: provider %q models: %w", providerKey, err)
		}
		for _, wm := range models {
			mid := strings.TrimSpace(wm.ID)
			if mid != "" {
				ids = append(ids, providerKey+"/"+mid)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func normalizeModelFacts(wm wireModel) modelcatalog.ModelFacts {
	f := modelcatalog.ModelFacts{
		Source:    modelcatalog.FactSourceCatalog,
		MatchKind: modelcatalog.MatchNone,
	}
	if wm.ToolCall != nil {
		if *wm.ToolCall {
			f.Tools = modelcatalog.CapabilitySupported
		} else {
			f.Tools = modelcatalog.CapabilityUnsupported
		}
	}
	if wm.Reasoning != nil {
		if *wm.Reasoning {
			f.Reasoning = modelcatalog.CapabilitySupported
		} else {
			f.Reasoning = modelcatalog.CapabilityUnsupported
		}
	}
	if wm.StructuredOutput != nil {
		if *wm.StructuredOutput {
			f.StructuredOutputs = modelcatalog.CapabilitySupported
		} else {
			f.StructuredOutputs = modelcatalog.CapabilityUnsupported
		}
	}
	if len(wm.Modalities) > 0 && string(wm.Modalities) != "null" {
		var m wireModalities
		if err := json.Unmarshal(wm.Modalities, &m); err == nil {
			for _, tok := range m.Input {
				switch strings.ToLower(strings.TrimSpace(tok)) {
				case "image":
					f.Vision = modelcatalog.CapabilitySupported
				case "pdf", "document", "file":
					f.Documents = modelcatalog.CapabilitySupported
				}
			}
		}
	}
	if wm.Limit != nil {
		f.ContextLimit = limitFromNumber(wm.Limit.Context)
		f.InputLimit = limitFromNumber(wm.Limit.Input)
		f.OutputLimit = limitFromNumber(wm.Limit.Output)
	}
	return f
}

func limitFromNumber(n json.Number) modelcatalog.LimitFact {
	if n == "" {
		return modelcatalog.LimitFact{State: modelcatalog.LimitUnknown}
	}
	v, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) || f >= float64(math.MaxInt64) {
			return modelcatalog.LimitFact{State: modelcatalog.LimitUnknown}
		}
		v = int64(f)
	}
	if v <= 0 {
		return modelcatalog.LimitFact{State: modelcatalog.LimitUnknown}
	}
	return modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: v}
}
