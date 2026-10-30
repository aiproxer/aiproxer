package repair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type OutcomeKind int

const (
	OutcomeUnspecified OutcomeKind = iota
	OutcomePass
	OutcomeRewrite
	OutcomeUnrepairable
)

type Input struct {
	ToolCallID string
	ToolName   string
	ArgsJSON   []byte
	// Tool is an optional exact-name resolved definition (common fast path).
	// When Tool.Name is non-empty and equals ToolName, Repair uses it without
	// indexing Catalog. Catalog is still required for normalized unique-name
	// matching when the exact tool is absent or name-mismatched.
	Tool         aipapi.ToolDef
	Catalog      []aipapi.ToolDef
	MaxArgsBytes int
}

type Outcome struct {
	Kind       OutcomeKind
	ToolName   string
	ArgsJSON   []byte
	ReasonCode string
}

// DefaultMaxArgsBytes is locked equal to the feature YAML default and runtime
// assembler fallback by TestDefaultMaxArgsBytesMatchCore /
// TestDefaultToolCallFinalizationMaxArgsBytesMatchCore.
const DefaultMaxArgsBytes = 64 * 1024

// Engine applies deterministic native tool-call repairs. It is safe for
// concurrent use; schema compilation uses the shared Phase 3 digest LRU cache.
type Engine struct {
	cache *SchemaCache
}

func NewEngine() *Engine {
	return &Engine{cache: packageSchemaCache()}
}

func NewEngineWithCache(cache *SchemaCache) *Engine {
	if cache == nil {
		cache = packageSchemaCache()
	}
	return &Engine{cache: cache}
}

func (e *Engine) Repair(in Input) (Outcome, error) {
	return e.RepairWithContext(context.Background(), in)
}

func (e *Engine) RepairWithContext(ctx context.Context, in Input) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := in.MaxArgsBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxArgsBytes
	}
	origArgs := in.ArgsJSON
	origName := in.ToolName

	fail := func(reason string) Outcome {
		var argsCopy []byte
		if origArgs != nil {
			argsCopy = append([]byte(nil), origArgs...)
		}
		return Outcome{
			Kind:       OutcomeUnrepairable,
			ToolName:   origName,
			ArgsJSON:   argsCopy,
			ReasonCode: reason,
		}
	}

	if err := ctx.Err(); err != nil {
		return fail(toolcall.ReasonCanceled), nil
	}
	if len(origArgs) > maxBytes {
		return fail(toolcall.ReasonArgsTooLarge), nil
	}

	tool, toolName, nameChanged, resolveFail := resolveRepairTool(in, origName)
	if resolveFail != "" {
		return fail(resolveFail), nil
	}

	args := origArgs
	syntaxChanged := false
	tailReason := ""
	var compiled *CompiledSchema
	var err error
	cache := e.cache
	if cache == nil {
		cache = packageSchemaCache()
	}
	if !json.Valid(args) {
		repaired, ok := CompleteJSONSuffix(args)
		if ok && len(repaired) <= maxBytes {
			args = repaired
			syntaxChanged = true
			tailReason = toolcall.ReasonSyntaxRepaired
		} else {
			analysis, classified := analyzeJSONTail(ctx, args)
			if !classified {
				if err := ctx.Err(); err != nil {
					return fail(toolcall.ReasonCanceled), nil
				}
				return fail(toolcall.ReasonUnrepairable), nil
			}
			switch analysis.kind {
			case tailRepairTrailingComma:
				candidate, ok := buildTrailingCommaCandidate(args, analysis, maxBytes)
				if !ok {
					return fail(toolcall.ReasonUnrepairable), nil
				}
				args = candidate
				syntaxChanged = true
				tailReason = toolcall.ReasonSyntaxRepaired
			case tailRepairPendingRootValue:
				if isEmptySchema(tool.Parameters) {
					return fail(toolcall.ReasonUnrepairable), nil
				}
				compiled, err = cache.GetOrCompileWithContext(ctx, tool.Parameters)
				if err != nil {
					if reason := mapEngineArgsShapeReason(err); reason == toolcall.ReasonCanceled {
						return fail(reason), nil
					}
					return fail(mapEngineSchemaReason(err)), nil
				}
				value, reason, ok, err := deterministicRootPendingValue(compiled, analysis.propertyName)
				if err != nil {
					var re *repairError
					if errors.As(err, &re) && re.reason != "" {
						return fail(re.reason), nil
					}
					return fail(toolcall.ReasonUnrepairable), nil
				}
				if !ok {
					return fail(toolcall.ReasonUnrepairable), nil
				}
				candidate, ok := buildPendingValueCandidate(args, analysis, value, maxBytes)
				if !ok {
					return fail(toolcall.ReasonUnrepairable), nil
				}
				args = candidate
				syntaxChanged = true
				tailReason = reason
			default:
				return fail(toolcall.ReasonUnrepairable), nil
			}
		}
	}
	if err := preflightArgsJSON(ctx, args, maxBytes); err != nil {
		return fail(mapEngineArgsShapeReason(err)), nil
	}

	if isEmptySchema(tool.Parameters) {
		return syntaxOnlyOutcome(toolName, origArgs, args, nameChanged, syntaxChanged), nil
	}
	if compiled == nil {
		compiled, err = cache.GetOrCompileWithContext(ctx, tool.Parameters)
		if err != nil {
			if reason := mapEngineArgsShapeReason(err); reason == toolcall.ReasonCanceled {
				return fail(reason), nil
			}
			return fail(mapEngineSchemaReason(err)), nil
		}
	}

	if err := compiled.validateWithMaxArgs(ctx, args, maxBytes); err == nil {
		if syntaxChanged {
			return Outcome{Kind: OutcomeRewrite, ToolName: toolName, ArgsJSON: bytes.Clone(args), ReasonCode: tailReason}, nil
		}
		return syntaxOnlyOutcome(toolName, origArgs, args, nameChanged, syntaxChanged), nil
	} else if reason := mapEngineArgsShapeReason(err); reason == toolcall.ReasonCanceled {
		return fail(reason), nil
	}

	if err := ctx.Err(); err != nil {
		return fail(toolcall.ReasonCanceled), nil
	}
	repaired, schemaReason, err := repairPreflightedArgsJSONDocument(ctx, args, compiled.orderedDocument, maxBytes)
	if err != nil {
		var re *repairError
		if errors.As(err, &re) && re.reason != "" {
			return fail(re.reason), nil
		}
		return fail(toolcall.ReasonUnrepairable), nil
	}

	candidate := args
	if schemaReason != "" {
		candidate = repaired
	}
	if schemaReason == "" && !syntaxChanged && !nameChanged {
		return fail(toolcall.ReasonUnrepairable), nil
	}
	if len(candidate) > maxBytes {
		return fail(toolcall.ReasonArgsTooLarge), nil
	}
	if err := preflightArgsJSON(ctx, candidate, maxBytes); err != nil {
		return fail(mapEngineArgsShapeReason(err)), nil
	}
	if err := compiled.validateWithMaxArgs(ctx, candidate, maxBytes); err != nil {
		return fail(toolcall.ReasonUnrepairable), nil
	}

	reason := schemaReason
	if syntaxChanged {
		// Tail syntax is the primary repair path; existing schema repairs are
		// secondary and must not obscure its stable public reason.
		reason = tailReason
	} else if reason == "" {
		reason = toolcall.ReasonToolNameNormalized
	}
	var outArgs []byte
	if schemaReason == "" && !syntaxChanged {
		outArgs = bytes.Clone(origArgs)
	} else {
		outArgs = bytes.Clone(candidate)
	}
	return Outcome{
		Kind:       OutcomeRewrite,
		ToolName:   toolName,
		ArgsJSON:   outArgs,
		ReasonCode: reason,
	}, nil
}

// resolveRepairTool prefers an exact-name resolved Tool when provided, else
// builds a CatalogIndex for exact/normalized lookup.
func resolveRepairTool(in Input, origName string) (tool aipapi.ToolDef, toolName string, nameChanged bool, failReason string) {
	toolName = origName
	if in.Tool.Name != "" && in.Tool.Name == origName {
		return cloneToolDef(in.Tool), toolName, false, ""
	}
	idx := BuildCatalogIndex(in.Catalog)
	if t, ok := idx.Exact(origName); ok {
		return t, toolName, false, ""
	}
	if t, matched := idx.UniqueNormalized(origName); matched {
		return t, t.Name, true, ""
	}
	if catalogNormAmbiguous(in.Catalog, origName) {
		return aipapi.ToolDef{}, toolName, false, toolcall.ReasonAmbiguousToolName
	}
	return aipapi.ToolDef{}, toolName, false, toolcall.ReasonUnrepairable
}

func catalogNormAmbiguous(catalog []aipapi.ToolDef, name string) bool {
	norm := NormalizeASCIIName(name)
	if norm == "" {
		return false
	}
	n := 0
	for _, tool := range catalog {
		if NormalizeASCIIName(tool.Name) == norm {
			n++
			if n > 1 {
				return true
			}
		}
	}
	return false
}

func mapEngineSchemaReason(err error) string {
	var se *SchemaError
	if !errors.As(err, &se) || se == nil {
		return toolcall.ReasonSchemaInvalid
	}
	if se.ReasonCode == ReasonCanceled {
		return toolcall.ReasonCanceled
	}
	switch se.Kind {
	case SchemaKindUnsupported:
		return toolcall.ReasonSchemaUnsupported
	default:
		return toolcall.ReasonSchemaInvalid
	}
}

func isEmptySchema(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return true
	}
	trim := bytes.TrimSpace(schema)
	return len(trim) == 0 || bytes.Equal(trim, []byte("null"))
}

func syntaxOnlyOutcome(toolName string, origArgs, args []byte, nameChanged, syntaxChanged bool) Outcome {
	if !syntaxChanged && !nameChanged {
		return Outcome{
			Kind:       OutcomePass,
			ToolName:   toolName,
			ReasonCode: toolcall.ReasonValidPassThrough,
		}
	}
	reason := toolcall.ReasonToolNameNormalized
	outArgs := bytes.Clone(origArgs)
	if syntaxChanged {
		reason = toolcall.ReasonSyntaxRepaired
		outArgs = bytes.Clone(args)
	}
	return Outcome{
		Kind:       OutcomeRewrite,
		ToolName:   toolName,
		ArgsJSON:   outArgs,
		ReasonCode: reason,
	}
}
