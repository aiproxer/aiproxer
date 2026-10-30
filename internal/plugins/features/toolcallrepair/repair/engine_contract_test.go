package repair_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type fixtureCatalogTool struct {
	Name          string          `json:"name"`
	Parameters    json.RawMessage `json:"parameters"`
	ParametersRaw string          `json:"parameters_raw"`
}

type fixtureFile struct {
	Name     string               `json:"name"`
	Catalog  []fixtureCatalogTool `json:"catalog"`
	Generate *struct {
		ArgsPrefix string `json:"args_prefix"`
		ArgsSuffix string `json:"args_suffix"`
		FillByte   byte   `json:"fill_byte"`
		FillCount  int    `json:"fill_count"`
	} `json:"generate"`
	Input struct {
		ToolName string `json:"tool_name"`
		ArgsJSON string `json:"args_json"`
	} `json:"input"`
	Expect struct {
		Action             string `json:"action"`
		ArgsJSON           string `json:"args_json"`
		ArgsJSONUnchanged  bool   `json:"args_json_unchanged"`
		ToolName           string `json:"tool_name"`
		ToolNameUnchanged  bool   `json:"tool_name_unchanged"`
		ReasonCode         string `json:"reason_code"`
		RequireSchemaValid bool   `json:"require_schema_valid"`
		JSONValid          bool   `json:"json_valid"`
		MustNotMutate      bool   `json:"must_not_mutate"`
	} `json:"expect"`
	Normalize *struct {
		Input string `json:"input"`
		Want  string `json:"want"`
	} `json:"normalize"`
}

type casesIndex struct {
	Cases []string `json:"cases"`
}

func TestEngineContract_Fixtures(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "testdata", "tool-call-repair")
	idxBytes, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	var idx casesIndex
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}
	if len(idx.Cases) == 0 {
		t.Fatal("cases.json listed no fixtures")
	}

	eng := repair.NewEngine()
	for _, name := range idx.Cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fx fixtureFile
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			if fx.Normalize != nil {
				got := repair.NormalizeASCIIName(fx.Normalize.Input)
				if got != fx.Normalize.Want {
					t.Fatalf("NormalizeASCIIName(%q)=%q want %q", fx.Normalize.Input, got, fx.Normalize.Want)
				}
			}

			args := []byte(fx.Input.ArgsJSON)
			if fx.Generate != nil {
				fill := bytes.Repeat([]byte{fx.Generate.FillByte}, fx.Generate.FillCount)
				args = append(append([]byte(fx.Generate.ArgsPrefix), fill...), []byte(fx.Generate.ArgsSuffix)...)
				if len(args) <= repair.DefaultMaxArgsBytes {
					t.Fatalf("oversized fixture must exceed DefaultMaxArgsBytes: got %d", len(args))
				}
			}
			catalog := make([]aipapi.ToolDef, 0, len(fx.Catalog))
			var matchedSchema json.RawMessage
			for _, c := range fx.Catalog {
				params := c.Parameters
				if c.ParametersRaw != "" {
					params = json.RawMessage(c.ParametersRaw)
				}
				catalog = append(catalog, aipapi.ToolDef{Name: c.Name, Parameters: params})
				if c.Name == fx.Input.ToolName || (fx.Expect.ToolName != "" && c.Name == fx.Expect.ToolName) {
					matchedSchema = params
				}
			}

			res, err := eng.Repair(repair.Input{
				ToolCallID:   "call-1",
				ToolName:     fx.Input.ToolName,
				ArgsJSON:     args,
				Catalog:      catalog,
				MaxArgsBytes: repair.DefaultMaxArgsBytes,
			})
			if err != nil {
				t.Fatalf("Repair: %v", err)
			}

			switch fx.Expect.Action {
			case "pass":
				if res.Kind != repair.OutcomePass {
					t.Fatalf("kind=%v want pass", res.Kind)
				}
			case "rewrite":
				if res.Kind != repair.OutcomeRewrite {
					t.Fatalf("kind=%v want rewrite", res.Kind)
				}
			case "unrepairable":
				if res.Kind != repair.OutcomeUnrepairable {
					t.Fatalf("kind=%v want unrepairable (policy mapping is not the engine's job)", res.Kind)
				}
			default:
				t.Fatalf("unknown expect.action %q", fx.Expect.Action)
			}

			if res.ReasonCode != fx.Expect.ReasonCode {
				t.Fatalf("reason_code=%q want %q", res.ReasonCode, fx.Expect.ReasonCode)
			}

			if fx.Expect.MustNotMutate && res.Kind == repair.OutcomeRewrite {
				t.Fatal("must not mutate: got rewrite")
			}
			if fx.Expect.ArgsJSONUnchanged {
				if res.ArgsJSON != nil && !bytes.Equal(res.ArgsJSON, args) {
					t.Fatalf("args mutated\nwant: %s\ngot:  %s", args, res.ArgsJSON)
				}
			}
			if fx.Expect.ToolNameUnchanged {
				if res.ToolName != "" && res.ToolName != fx.Input.ToolName {
					t.Fatalf("tool name mutated: got %q", res.ToolName)
				}
			}
			if fx.Expect.ToolName != "" && fx.Expect.Action == "rewrite" {
				if res.ToolName != fx.Expect.ToolName {
					t.Fatalf("tool_name=%q want %q", res.ToolName, fx.Expect.ToolName)
				}
			}
			if fx.Expect.ArgsJSON != "" {
				if !bytes.Equal(res.ArgsJSON, []byte(fx.Expect.ArgsJSON)) {
					t.Fatalf("args_json mismatch\nwant: %s\ngot:  %s", fx.Expect.ArgsJSON, res.ArgsJSON)
				}
			}
			if fx.Expect.JSONValid || fx.Expect.Action == "rewrite" {
				target := res.ArgsJSON
				if len(target) == 0 {
					target = args
				}
				if !json.Valid(target) {
					t.Fatalf("result args not JSON-valid: %s", target)
				}
			}
			if fx.Expect.RequireSchemaValid {
				target := res.ArgsJSON
				if len(target) == 0 {
					target = args
				}
				schema := matchedSchema
				if fx.Expect.ToolName != "" {
					for _, c := range catalog {
						if c.Name == fx.Expect.ToolName {
							schema = c.Parameters
							break
						}
					}
				}
				if err := repair.ValidateArgsAgainstSchema(target, schema); err != nil {
					t.Fatalf("RequireSchemaValid: %v", err)
				}
			}
			if fx.Expect.Action == "pass" && fx.Expect.ArgsJSONUnchanged {
				if res.ArgsJSON != nil && !bytes.Equal(res.ArgsJSON, args) {
					t.Fatalf("valid pass must preserve exact args bytes")
				}
			}
		})
	}
}

func TestNormalizeASCIIName_Contract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "hyphen_case", in: "Get-Weather", want: "getweather"},
		{name: "underscore", in: "get_weather", want: "getweather"},
		{name: "spaces", in: "Get Weather", want: "getweather"},
		{name: "preserves_other_bytes", in: "tool.v2", want: "tool.v2"},
		{name: "preserves_non_ascii", in: "Caf\u00e9", want: "caf\u00e9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := repair.NormalizeASCIIName(tc.in)
			if got != tc.want {
				t.Fatalf("NormalizeASCIIName(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateArgsAgainstSchema_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	if err := repair.ValidateArgsAgainstSchema([]byte(`{"location":"NYC"}`), schema); err != nil {
		t.Fatalf("valid args: %v", err)
	}
	err := repair.ValidateArgsAgainstSchema([]byte(`{"location":1}`), schema)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	var se *repair.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("got %T want *SchemaError", err)
	}
	if se.Kind != repair.SchemaKindValidationFailed {
		t.Fatalf("kind=%q want %q", se.Kind, repair.SchemaKindValidationFailed)
	}
}

func TestReasonCodesArePublicRepairSet(t *testing.T) {
	t.Parallel()
	codes := []string{
		toolcall.ReasonValidPassThrough,
		toolcall.ReasonSyntaxRepaired,
		toolcall.ReasonToolNameNormalized,
		toolcall.ReasonPropertyRenamed,
		toolcall.ReasonDefaultInserted,
		toolcall.ReasonConstInserted,
		toolcall.ReasonEnumInserted,
		toolcall.ReasonAdditionalPropertyRemoved,
		toolcall.ReasonAmbiguousToolName,
		toolcall.ReasonAmbiguousProperty,
		toolcall.ReasonUnrepairable,
		toolcall.ReasonSchemaInvalid,
		toolcall.ReasonSchemaUnsupported,
		toolcall.ReasonArgsTooLarge,
		toolcall.ReasonScalarCoercionDisabled,
	}
	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		if c == "" {
			t.Fatal("public reason code must be non-empty")
		}
		if _, ok := seen[c]; ok {
			t.Fatalf("duplicate public reason code %q", c)
		}
		seen[c] = struct{}{}
	}
}
