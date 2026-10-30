package openresponsescompat

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestCodecOptions_defaultsToGenericMode(t *testing.T) {
	t.Parallel()
	opts := DefaultCodecOptions()
	if opts.Profile() != DefaultProfile {
		t.Fatalf("Profile()=%q, want %q", opts.Profile(), DefaultProfile)
	}
	if opts.FactoryKind() != ID {
		t.Fatalf("FactoryKind()=%q, want %q", opts.FactoryKind(), ID)
	}
	if opts.ProviderID() != "" {
		t.Fatalf("ProviderID()=%q, want empty generic mode", opts.ProviderID())
	}
}

func TestCodecOptions_newNormalizesProfileAndFactoryKind(t *testing.T) {
	t.Parallel()
	opts, err := NewCodecOptions("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Profile() != DefaultProfile {
		t.Fatalf("Profile()=%q", opts.Profile())
	}
	if opts.FactoryKind() != ID {
		t.Fatalf("FactoryKind()=%q", opts.FactoryKind())
	}

	opts, err = NewCodecOptions(DefaultProfile, "custom-provider-compat", "provider-x")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Profile() != DefaultProfile {
		t.Fatalf("Profile()=%q", opts.Profile())
	}
	if opts.FactoryKind() != "custom-provider-compat" {
		t.Fatalf("FactoryKind()=%q", opts.FactoryKind())
	}
	if opts.ProviderID() != "provider-x" {
		t.Fatalf("ProviderID()=%q", opts.ProviderID())
	}
}

func TestCodecOptions_rejectsUnsupportedProfile(t *testing.T) {
	t.Parallel()
	_, err := NewCodecOptions("2025-01-01", ID, "")
	if err == nil {
		t.Fatal("expected unsupported profile rejection")
	}
	if !strings.Contains(err.Error(), "2025-01-01") || !strings.Contains(err.Error(), DefaultProfile) {
		t.Fatalf("error = %v, want profile and supported profile listed", err)
	}
}

func TestCodecOptions_rejectsProviderPolicyIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "uppercase", id: "OpenRouter"},
		{name: "slash_route", id: "openrouter/route"},
		{name: "colon_billing", id: "billing:plan"},
		{name: "space", id: "provider x"},
		{name: "starts_separator", id: "-provider"},
		{name: "ends_separator", id: "provider-"},
		{name: "too_long", id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "control", id: "provider\x00x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewCodecOptions(DefaultProfile, ID, tc.id); err == nil {
				t.Fatalf("expected provider id %q rejection", tc.id)
			}
		})
	}
}

func TestCodecOptions_immutableAndConcurrencySafe(t *testing.T) {
	t.Parallel()
	opts, err := NewCodecOptions(DefaultProfile, ID, "provider-x")
	if err != nil {
		t.Fatal(err)
	}
	// No exported fields; only read accessors exist.
	rt := reflect.TypeFor[CodecOptions]()
	if rt.NumField() != 3 {
		t.Fatalf("CodecOptions fields = %d, want 3 unexported", rt.NumField())
	}
	for field := range rt.Fields() {
		if field.PkgPath == "" {
			t.Fatalf("field %s is exported; CodecOptions must be immutable via accessors", field.Name)
		}
	}
	// Concurrent readers observe stable provenance.
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				if opts.Profile() != DefaultProfile {
					t.Error("Profile() changed under concurrent read")
					return
				}
				if opts.ProviderID() != "provider-x" {
					t.Error("ProviderID() changed under concurrent read")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestCodecOptions_noArbitraryCallbacks(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[CodecOptions]()
	for f := range rt.Fields() {
		if f.Type.Kind() == reflect.Func || f.Type.Kind() == reflect.Interface {
			t.Fatalf("CodecOptions field %s has kind %s; arbitrary callbacks are forbidden", f.Name, f.Type.Kind())
		}
	}
}
