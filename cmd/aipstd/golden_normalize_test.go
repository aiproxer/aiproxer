package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

// normalizeDogfoodRoutesJSONForGolden sorts slice fields whose order is not contractually stable
// so CLI golden compares remain deterministic when enumeration order changes.
func normalizeDogfoodRoutesJSONForGolden(raw []byte) ([]byte, error) {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	sortNamedObjectSlice(v, "backends", "id")
	sortNamedObjectSlice(v, "model_aliases", "alias")
	return json.Marshal(v)
}

// normalizeDogfoodInventoryJSONForGolden sorts inventory slices used for stable golden comparison.
func normalizeDogfoodInventoryJSONForGolden(raw []byte) ([]byte, error) {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	sortNamedObjectSlice(v, "frontends", "id")
	sortNamedObjectSlice(v, "backends", "id")
	sortNamedObjectSlice(v, "features", "id")
	ext, ok := v["extensions"].(map[string]any)
	if ok {
		sortNamedObjectSlice(ext, "stages", "id")
		sortNamedObjectSlice(ext, "features", "instance_id")
		if feats, ok := ext["features"].([]any); ok {
			for _, item := range feats {
				fm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				sortNamedObjectSlice(fm, "stage_occupancy", "stage_id")
			}
		}
	}
	return json.Marshal(v)
}

func sortNamedObjectSlice(container map[string]any, key, idField string) {
	arr, ok := container[key].([]any)
	if !ok || len(arr) == 0 {
		return
	}
	slices.SortFunc(arr, func(a, b any) int {
		ma, okA := a.(map[string]any)
		mb, okB := b.(map[string]any)
		if !okA || !okB {
			return 0
		}
		sa, _ := ma[idField].(string)
		sb, _ := mb[idField].(string)
		return cmp.Compare(sa, sb)
	})
	container[key] = arr
}

func TestRunCommand_routes_dogfoodLocalStub_matchesGoldenJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "dogfood-local-stub", "routes.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	gotNorm, err := normalizeDogfoodRoutesJSONForGolden(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	wantNorm, err := normalizeDogfoodRoutesJSONForGolden(golden)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertJSONEqual(t, wantNorm, gotNorm)
}

func TestRunCommand_inventory_dogfoodLocalStub_matchesGoldenJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "dogfood-local-stub", "inventory.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	gotNorm, err := normalizeDogfoodInventoryJSONForGolden(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	wantNorm, err := normalizeDogfoodInventoryJSONForGolden(golden)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertJSONEqual(t, wantNorm, gotNorm)
}
