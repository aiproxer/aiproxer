package b2bua_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/continuity"
)

func TestContinuityContract_ALegRecordFieldsMatchSDK(t *testing.T) {
	t.Parallel()
	assertStructFieldsMatch(t, reflect.TypeFor[b2bua.ALegRecord](), reflect.TypeFor[continuity.ALegRecord]())
}

func TestContinuityContract_BLegRecordFieldsMatchSDK(t *testing.T) {
	t.Parallel()
	assertStructFieldsMatch(t, reflect.TypeFor[b2bua.BLegRecord](), reflect.TypeFor[continuity.BLegRecord]())
}

func TestContinuityContract_StoreInterfaceMatchesSDK(t *testing.T) {
	t.Parallel()
	core := reflect.TypeFor[b2bua.Store]()
	sdk := reflect.TypeFor[continuity.Store]()
	if core.Kind() != reflect.Interface || sdk.Kind() != reflect.Interface {
		t.Fatalf("Store types must be interfaces")
	}
	if core.NumMethod() != sdk.NumMethod() {
		t.Fatalf("method count core=%d sdk=%d", core.NumMethod(), sdk.NumMethod())
	}
	for i := range core.NumMethod() {
		cm := core.Method(i)
		sm := sdk.Method(i)
		if cm.Name != sm.Name {
			t.Fatalf("method %d name core=%q sdk=%q", i, cm.Name, sm.Name)
		}
		cSig := normalizeContinuityTypeString(cm.Type.String())
		sSig := normalizeContinuityTypeString(sm.Type.String())
		if cSig != sSig {
			t.Fatalf("method %s signature drift:\ncore: %s\n sdk: %s", cm.Name, cSig, sSig)
		}
	}
}

func assertStructFieldsMatch(t *testing.T, core, sdk reflect.Type) {
	t.Helper()
	if core.Kind() != reflect.Struct || sdk.Kind() != reflect.Struct {
		t.Fatalf("want struct types, got %v and %v", core.Kind(), sdk.Kind())
	}
	if core.NumField() != sdk.NumField() {
		t.Fatalf("field count core=%d sdk=%d", core.NumField(), sdk.NumField())
	}
	for i := range core.NumField() {
		cf := core.Field(i)
		sf := sdk.Field(i)
		if cf.Name != sf.Name {
			t.Fatalf("field %d name core=%q sdk=%q", i, cf.Name, sf.Name)
		}
		if cf.Type.String() != sf.Type.String() {
			t.Fatalf("field %s type core=%q sdk=%q", cf.Name, cf.Type.String(), sf.Type.String())
		}
	}
}

func normalizeContinuityTypeString(s string) string {
	s = strings.ReplaceAll(s, "github.com/aiproxer/aiproxer/internal/core/b2bua.", "")
	s = strings.ReplaceAll(s, "github.com/aiproxer/aiproxer/pkg/aipsdk/continuity.", "")
	s = strings.ReplaceAll(s, "b2bua.", "")
	s = strings.ReplaceAll(s, "continuity.", "")
	return s
}
