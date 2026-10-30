package runtimebundle

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const testBackendSecret = "secret-value-must-not-escape"

func TestBackendResourceConstruction_ForwardsSecretBundleToDial(t *testing.T) {
	t.Parallel()

	input := testBackendResourcePhysicalInput()
	input.RuntimePolicy.MaxRequestBytes = 4096
	identity, shareable, err := physicalIdentity(input)
	if err != nil {
		t.Fatalf("physical identity: %v", err)
	}
	if !shareable || identity == (backendResourceIdentity{}) {
		t.Fatal("secret-bearing per_instance input must produce a physical identity")
	}

	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9015},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	var got backendplugin.SecretBundle
	dial := func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		got = req.Secrets
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return inst, profile, nil
	}

	backend, cleanup, err := buildDiscoveredPhysical(
		context.Background(),
		host,
		ValidatedExport{
			Kind:     input.FactoryKind,
			Artifact: &trust.VerifiedArtifact{DigestHex: input.ArtifactDigest},
			Model:    input.ProcessModel,
		},
		input,
		dial,
		new(atomic.Uint64),
		nil,
	)
	if err != nil {
		t.Fatalf("build discovered physical resource: %v", err)
	}
	if backend.Open == nil {
		t.Fatal("expected adapter backend after in-process configure")
	}
	t.Cleanup(func() { _ = cleanup() })

	if !reflect.DeepEqual(got, input.Secrets) {
		t.Fatal("DialSessionRequest received a different secret bundle than the physical input")
	}
}

// TestBackendResourceIdentity_AllPhysicalInputsAreDiscriminating freezes the
// private physical identity boundary before the pool is implemented.  A
// false-negative (building twice) is safe; a false-positive (reusing a
// configured connector for a different physical input) is not.
func TestBackendResourceIdentity_AllPhysicalInputsAreDiscriminating(t *testing.T) {
	t.Parallel()

	base := testBackendResourcePhysicalInput()
	baseID, baseShareable, err := physicalIdentity(base)
	if err != nil {
		t.Fatalf("base physical identity: %v", err)
	}
	if !baseShareable {
		t.Fatal("per_instance input must be eligible for pooling")
	}

	tests := []struct {
		name          string
		mutate        func(*backendResourcePhysicalInput)
		wantShareable bool
	}{
		{
			name: "logical instance",
			mutate: func(in *backendResourcePhysicalInput) {
				in.InstanceID = "instance-b"
			},
			wantShareable: true,
		},
		{
			name: "factory kind",
			mutate: func(in *backendResourcePhysicalInput) {
				in.FactoryKind = "factory-b"
			},
			wantShareable: true,
		},
		{
			name: "verified artifact digest",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ArtifactDigest = "sha256:artifact-b"
			},
			wantShareable: true,
		},
		{
			name: "process model",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ProcessModel = processhost.ProcessModelSharedArtifact
			},
			wantShareable: false,
		},
		{
			name: "opaque Configure bytes",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ConfigureYAML = []byte("opaque:\n  value: two\n")
			},
			wantShareable: true,
		},
		{
			name: "normalized RuntimePolicy",
			mutate: func(in *backendResourcePhysicalInput) {
				in.RuntimePolicy.MaxPendingEvents++
			},
			wantShareable: true,
		},
		{
			name: "secret material",
			mutate: func(in *backendResourcePhysicalInput) {
				in.Secrets.Values["api_key"] = []byte("rotated-secret")
			},
			wantShareable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneBackendResourcePhysicalInput(base)
			tt.mutate(&input)
			got, shareable, err := physicalIdentity(input)
			if err != nil {
				t.Fatalf("physical identity: %v", err)
			}
			if shareable != tt.wantShareable {
				t.Fatalf("shareable=%v, want %v", shareable, tt.wantShareable)
			}
			if tt.wantShareable && reflect.DeepEqual(baseID, got) {
				t.Fatalf("physical input difference %q aliased base identity: %#v", tt.name, got)
			}
		})
	}
}

// TestBackendResourceIdentity_EligibleDifferencesMissPoolAndBuildOnce proves
// the final construction seam, not just the digest projection. Every
// shareable physical-input difference must miss the existing private pool
// entry and create exactly one fresh physical resource. The repeated acquire
// confirms that the fresh identity itself remains reusable.
func TestBackendResourceIdentity_EligibleDifferencesMissPoolAndBuildOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*backendResourcePhysicalInput)
	}{
		{
			name: "logical instance",
			mutate: func(in *backendResourcePhysicalInput) {
				in.InstanceID = "instance-b"
			},
		},
		{
			name: "factory kind",
			mutate: func(in *backendResourcePhysicalInput) {
				in.FactoryKind = "factory-b"
			},
		},
		{
			name: "artifact digest",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ArtifactDigest = "sha256:artifact-b"
			},
		},
		{
			name: "opaque Configure bytes",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ConfigureYAML = []byte("opaque:\n  value: two\n")
			},
		},
		{
			name: "normalized RuntimePolicy value",
			mutate: func(in *backendResourcePhysicalInput) {
				in.RuntimePolicy.MaxPendingEvents++
			},
		},
		{
			name: "secret fingerprint",
			mutate: func(in *backendResourcePhysicalInput) {
				in.Secrets.Values["api_key"] = []byte("rotated-secret")
			},
		},
	}

	base := testBackendResourcePhysicalInput()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseInput := cloneBackendResourcePhysicalInput(base)
			changedInput := cloneBackendResourcePhysicalInput(base)
			tt.mutate(&changedInput)

			baseID, baseShareable, err := physicalIdentity(baseInput)
			if err != nil {
				t.Fatalf("base physical identity: %v", err)
			}
			changedID, changedShareable, err := physicalIdentity(changedInput)
			if err != nil {
				t.Fatalf("changed physical identity: %v", err)
			}
			if !baseShareable || !changedShareable {
				t.Fatal("eligible physical-input matrix entries must be shareable")
			}
			if reflect.DeepEqual(baseID, changedID) {
				t.Fatalf("%s aliased the base physical identity", tt.name)
			}

			pool := newBackendResourcePool()
			probe := &backendResourcePoolProbe{}
			baseLease, err := pool.Acquire(context.Background(), baseID, probe.build)
			if err != nil {
				t.Fatalf("base Acquire: %v", err)
			}
			changedLease, err := pool.Acquire(context.Background(), changedID, probe.build)
			if err != nil {
				_ = baseLease.Cleanup()
				t.Fatalf("changed Acquire: %v", err)
			}

			if got := probe.builds.Load(); got != 2 {
				t.Fatalf("%s physical builds=%d, want exactly 2 after identity miss", tt.name, got)
			}
			baseEntry, baseClaims, baseOwned := backendResourcePoolSnapshot(t, pool, baseID)
			changedEntry, changedClaims, changedOwned := backendResourcePoolSnapshot(t, pool, changedID)
			if baseEntry == nil || changedEntry == nil {
				t.Fatalf("%s pool entries base=%v changed=%v, want both current", tt.name, baseEntry != nil, changedEntry != nil)
			}
			if baseEntry == changedEntry {
				t.Fatalf("%s reused the existing private pool entry", tt.name)
			}
			if baseClaims != 1 || changedClaims != 1 || baseOwned != 2 || changedOwned != 2 {
				t.Fatalf("%s pool claims/ownership base=%d/%d changed=%d/%d, want 1/2 and 1/2", tt.name, baseClaims, baseOwned, changedClaims, changedOwned)
			}

			reusedChangedLease, err := pool.Acquire(context.Background(), changedID, probe.build)
			if err != nil {
				_ = changedLease.Cleanup()
				_ = baseLease.Cleanup()
				t.Fatalf("changed identity reuse Acquire: %v", err)
			}
			if got := probe.builds.Load(); got != 2 {
				t.Fatalf("%s repeated changed acquire builds=%d, want exactly 2", tt.name, got)
			}

			if err := reusedChangedLease.Cleanup(); err != nil {
				t.Fatalf("repeated changed lease cleanup: %v", err)
			}
			if err := changedLease.Cleanup(); err != nil {
				t.Fatalf("changed lease cleanup: %v", err)
			}
			if err := baseLease.Cleanup(); err != nil {
				t.Fatalf("base lease cleanup: %v", err)
			}
			if got := probe.cleanups.Load(); got != 2 {
				t.Fatalf("%s physical cleanups=%d, want exactly 2", tt.name, got)
			}
			if err := pool.Close(); err != nil {
				t.Fatalf("pool close: %v", err)
			}
		})
	}
}

// TestBackendResourceIdentity_IncompletePhysicalInputsFailClosed ensures that
// incomplete identity inputs can never authorize pooled reuse.  An
// implementation may reject them with an error or deliberately fall back to
// isolated construction; both are fail-closed outcomes.  Nil/empty Configure
// bytes and nil Secrets are intentionally not included because they can be
// valid effective inputs for a connector.
func TestBackendResourceIdentity_IncompletePhysicalInputsFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*backendResourcePhysicalInput)
	}{
		{
			name: "blank logical instance id",
			mutate: func(in *backendResourcePhysicalInput) {
				in.InstanceID = ""
			},
		},
		{
			name: "whitespace logical instance id",
			mutate: func(in *backendResourcePhysicalInput) {
				in.InstanceID = "  \t\n"
			},
		},
		{
			name: "blank factory kind",
			mutate: func(in *backendResourcePhysicalInput) {
				in.FactoryKind = ""
			},
		},
		{
			name: "whitespace factory kind",
			mutate: func(in *backendResourcePhysicalInput) {
				in.FactoryKind = " \t\n"
			},
		},
		{
			name: "blank artifact digest",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ArtifactDigest = ""
			},
		},
		{
			name: "whitespace artifact digest",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ArtifactDigest = "  \t\n"
			},
		},
		{
			name: "empty process model",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ProcessModel = ""
			},
		},
		{
			name: "unknown process model",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ProcessModel = processhost.ProcessModel("future_model")
			},
		},
	}

	base := testBackendResourcePhysicalInput()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneBackendResourcePhysicalInput(base)
			tt.mutate(&input)
			_, shareable, err := physicalIdentity(input)
			if err == nil && shareable {
				t.Fatalf("incomplete input %q authorized shareable reuse", tt.name)
			}
		})
	}
}

// TestBackendResourceIdentity_NonEligibleModelsUseIsolatedFallback pins the
// process-model boundary.  shared_artifact is not a pooled replacement case;
// unsupported models must also remain on current generation-local construction.
func TestBackendResourceIdentity_NonEligibleModelsUseIsolatedFallback(t *testing.T) {
	t.Parallel()

	for _, model := range []processhost.ProcessModel{
		processhost.ProcessModelSharedArtifact,
		processhost.ProcessModel("future_model"),
	} {
		input := testBackendResourcePhysicalInput()
		input.ProcessModel = model
		_, shareable, err := physicalIdentity(input)
		if err != nil {
			t.Fatalf("model %q physical identity: %v", model, err)
		}
		if shareable {
			t.Fatalf("model %q must use isolated construction, not pooled reuse", model)
		}
	}
}

// TestBackendResourceIdentity_NormalizesRuntimePolicy freezes deterministic
// effective-policy projection. AllowedEnvNames is set-like at this boundary,
// so ordering and duplicate presentation must not change identity.
func TestBackendResourceIdentity_NormalizesRuntimePolicy(t *testing.T) {
	t.Parallel()

	a := testBackendResourcePhysicalInput()
	b := cloneBackendResourcePhysicalInput(a)
	b.RuntimePolicy.AllowedEnvNames = []string{"TMPDIR", "PATH", "PATH"}
	a.RuntimePolicy.AllowedEnvNames = []string{"PATH", "TMPDIR"}

	idA, shareableA, err := physicalIdentity(a)
	if err != nil {
		t.Fatalf("policy A physical identity: %v", err)
	}
	idB, shareableB, err := physicalIdentity(b)
	if err != nil {
		t.Fatalf("policy B physical identity: %v", err)
	}
	if !shareableA || !shareableB {
		t.Fatal("normalized per_instance policies must remain shareable")
	}
	if !reflect.DeepEqual(idA, idB) {
		t.Fatalf("semantically equivalent policies produced different identities: A=%#v B=%#v", idA, idB)
	}
}

// TestBackendResourceIdentity_SecretFingerprintIsDeterministicAndPrivate
// proves equality without plaintext retention.  The test deliberately checks
// both normal debug formatting and JSON-shaped status rendering because an
// opaque private identity must not become a secret-bearing diagnostic value.
func TestBackendResourceIdentity_SecretFingerprintIsDeterministicAndPrivate(t *testing.T) {
	t.Parallel()

	a := testBackendResourcePhysicalInput()
	b := cloneBackendResourcePhysicalInput(a)
	// Map insertion order must not affect the fingerprint.
	b.Secrets.Values = map[string][]byte{
		"tenant":  []byte("tenant-a"),
		"api_key": []byte(testBackendSecret),
	}

	idA, shareableA, err := physicalIdentity(a)
	if err != nil {
		t.Fatalf("secret A physical identity: %v", err)
	}
	idB, shareableB, err := physicalIdentity(b)
	if err != nil {
		t.Fatalf("secret B physical identity: %v", err)
	}
	if !shareableA || !shareableB {
		t.Fatal("secret-bearing per_instance inputs must remain shareable")
	}
	if !reflect.DeepEqual(idA, idB) {
		t.Fatalf("same secret material produced different identities: A=%#v B=%#v", idA, idB)
	}
	assertIdentityOmitsSecret(t, idA, testBackendSecret)

	c := cloneBackendResourcePhysicalInput(a)
	c.Secrets.Values["api_key"] = []byte("rotated-secret")
	idC, shareableC, err := physicalIdentity(c)
	if err != nil {
		t.Fatalf("rotated secret physical identity: %v", err)
	}
	if !shareableC {
		t.Fatal("rotated secret input must remain eligible for isolated-or-pooled decision")
	}
	if reflect.DeepEqual(idA, idC) {
		t.Fatal("secret rotation aliased the previous physical identity")
	}
	assertIdentityOmitsSecret(t, idC, "rotated-secret")
}

// TestBackendResourceIdentity_SecretFingerprintUsesLengthFraming guards the
// local secret projection against concatenation collisions such as
// name="ab", value="c" versus name="a", value="bc".
func TestBackendResourceIdentity_SecretFingerprintUsesLengthFraming(t *testing.T) {
	t.Parallel()

	a := testBackendResourcePhysicalInput()
	a.Secrets.Values = map[string][]byte{"ab": []byte("c")}
	b := cloneBackendResourcePhysicalInput(a)
	b.Secrets.Values = map[string][]byte{"a": []byte("bc")}

	idA, shareableA, err := physicalIdentity(a)
	if err != nil {
		t.Fatalf("secret framing A physical identity: %v", err)
	}
	idB, shareableB, err := physicalIdentity(b)
	if err != nil {
		t.Fatalf("secret framing B physical identity: %v", err)
	}
	if !shareableA || !shareableB {
		t.Fatal("secret framing candidates must remain shareable")
	}
	if reflect.DeepEqual(idA, idB) {
		t.Fatal("different length-framed secret name/value pairs aliased")
	}
}

// TestBackendResourceIdentity_BackendStateIdentityIsInsufficientForReuse
// keeps the existing affinity/health key explicitly separate from physical
// connector reuse.  Equal BackendStateIdentity values are not proof that the
// configured artifact, process, policy, or secrets are interchangeable.
func TestBackendResourceIdentity_BackendStateIdentityIsInsufficientForReuse(t *testing.T) {
	t.Parallel()

	stateA := BackendStateIdentity{InstanceID: "instance-a", FactoryKind: "factory-a", ConfigDigest: "same"}
	stateB := stateA
	if !stateA.Compatible(stateB) {
		t.Fatal("control: equal BackendStateIdentity values must be compatible")
	}

	a := testBackendResourcePhysicalInput()
	b := cloneBackendResourcePhysicalInput(a)
	b.ArtifactDigest = "sha256:replacement"
	b.Secrets.Values["api_key"] = []byte("rotated-secret")
	idA, shareableA, err := physicalIdentity(a)
	if err != nil {
		t.Fatalf("physical identity A: %v", err)
	}
	idB, shareableB, err := physicalIdentity(b)
	if err != nil {
		t.Fatalf("physical identity B: %v", err)
	}
	if !shareableA || !shareableB {
		t.Fatal("control inputs must be shareable candidates")
	}
	if reflect.DeepEqual(idA, idB) {
		t.Fatal("BackendStateIdentity-compatible inputs must not alias after physical artifact/secret changes")
	}
}

// TestBackendResourceIdentity_StartupFixedInputsAreFocusedOnly records the
// evidence split: artifact, process model, and install-time policy are tested
// at the physical identity seam, but this test does not claim those dimensions
// are hot-reloadable in the current discovered-factory path.
func TestBackendResourceIdentity_StartupFixedInputsAreFocusedOnly(t *testing.T) {
	t.Parallel()

	base := testBackendResourcePhysicalInput()
	for _, tt := range []struct {
		name   string
		mutate func(*backendResourcePhysicalInput)
	}{
		{
			name: "artifact digest",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ArtifactDigest = "sha256:startup-replaced"
			},
		},
		{
			name: "process model",
			mutate: func(in *backendResourcePhysicalInput) {
				in.ProcessModel = processhost.ProcessModelSharedArtifact
			},
		},
		{
			name: "install-time runtime policy",
			mutate: func(in *backendResourcePhysicalInput) {
				in.RuntimePolicy.RequestTimeoutMS++
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneBackendResourcePhysicalInput(base)
			tt.mutate(&input)
			got, shareable, err := physicalIdentity(input)
			if err != nil {
				t.Fatalf("focused physical identity: %v", err)
			}
			baseID, _, baseErr := physicalIdentity(base)
			if baseErr != nil {
				t.Fatalf("base physical identity: %v", baseErr)
			}
			if shareable && reflect.DeepEqual(baseID, got) {
				t.Fatalf("startup-fixed difference %q aliased a pooled identity", tt.name)
			}
			if tt.name == "process model" && shareable {
				t.Fatal("shared_artifact must remain non-pooled")
			}
		})
	}
}

// TestBackendResourceIdentity_PhysicalInputDTORequiresExplicitReview is a
// test-only structural drift gate.  Production identity construction remains
// explicit (not reflection-driven); adding a configure-time physical input
// requires updating this contract and making an intentional identity decision.
func TestBackendResourceIdentity_PhysicalInputDTORequiresExplicitReview(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[backendResourcePhysicalInput]()
	want := map[string]reflect.Type{
		"InstanceID":               reflect.TypeFor[string](),
		"FactoryKind":              reflect.TypeFor[string](),
		"ArtifactDigest":           reflect.TypeFor[string](),
		"ProcessModel":             reflect.TypeFor[processhost.ProcessModel](),
		"ConfigureYAML":            reflect.TypeFor[[]byte](),
		"RuntimePolicy":            reflect.TypeFor[backendplugin.RuntimePolicy](),
		"Secrets":                  reflect.TypeFor[backendplugin.SecretBundle](),
		"LaunchEnv":                reflect.TypeFor[map[string]string](),
		"CredentialEnvRoot":        reflect.TypeFor[string](),
		"CredentialQuotaDomain":    reflect.TypeFor[string](),
		"CredentialSlot":           reflect.TypeFor[string](),
		"CredentialSelection":      reflect.TypeFor[string](),
		"CredentialPolicyRevision": reflect.TypeFor[string](),
		// The connector economic evidence policy changes the adapter's
		// observable behaviour, so it is framed into the physical identity
		// like the runtime policy: two runtimes with different policies must
		// never share one pooled connector resource.
		"ConnectorEvidencePolicy": reflect.TypeFor[execbackend.ConnectorEconomicEvidencePolicy](),
	}
	if typ.NumField() != len(want) {
		t.Fatalf("physical input DTO has %d fields; expected %d: review every new configure/launch input for identity treatment", typ.NumField(), len(want))
	}
	for name, wantType := range want {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("physical input DTO missing %s; update identity contract deliberately", name)
		}
		if field.Type != wantType {
			t.Fatalf("physical input DTO field %s has type %s, want %s; review identity treatment", name, field.Type, wantType)
		}
	}
}

func testBackendResourcePhysicalInput() backendResourcePhysicalInput {
	return backendResourcePhysicalInput{
		InstanceID:     "instance-a",
		FactoryKind:    "factory-a",
		ArtifactDigest: "sha256:artifact-a",
		ProcessModel:   processhost.ProcessModelPerInstance,
		ConfigureYAML:  []byte("opaque:\n  value: one\n"),
		RuntimePolicy: backendplugin.RuntimePolicy{
			MaxRequestBytes:         1024,
			MaxStreamFrameBytes:     2048,
			MaxPendingEvents:        8,
			RequestTimeoutMS:        5000,
			CancelDeadlineMS:        1000,
			DiagnosticsVerbosity:    "normal",
			MaxConcurrentExecutions: 2,
			LocalOnly:               true,
			AllowedEnvNames:         []string{"PATH", "TMPDIR"},
			DisableTransportRetries: true,
		},
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{
			"api_key": []byte(testBackendSecret),
			"tenant":  []byte("tenant-a"),
		}},
	}
}

func cloneBackendResourcePhysicalInput(in backendResourcePhysicalInput) backendResourcePhysicalInput {
	out := in
	out.ConfigureYAML = append([]byte(nil), in.ConfigureYAML...)
	out.RuntimePolicy.AllowedEnvNames = append([]string(nil), in.RuntimePolicy.AllowedEnvNames...)
	out.Secrets.Values = make(map[string][]byte, len(in.Secrets.Values))
	for name, value := range in.Secrets.Values {
		out.Secrets.Values[name] = append([]byte(nil), value...)
	}
	out.LaunchEnv = make(map[string]string, len(in.LaunchEnv))
	maps.Copy(out.LaunchEnv, in.LaunchEnv)
	return out
}

func assertIdentityOmitsSecret(t *testing.T, identity backendResourceIdentity, secret string) {
	t.Helper()
	debug := fmt.Sprintf("%v %#v", identity, identity)
	if strings.Contains(debug, secret) {
		t.Fatalf("identity debug output contains secret plaintext %q: %s", secret, debug)
	}
	//nolint:staticcheck // SA9005: verifying JSON marshaling of identity struct does not leak secrets
	status, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("identity JSON status rendering: %v", err)
	}
	if strings.Contains(string(status), secret) {
		t.Fatalf("identity JSON status contains secret plaintext %q: %s", secret, status)
	}
	if reflectValueContainsBytes(reflect.ValueOf(identity), []byte(secret)) {
		t.Fatalf("identity retains secret plaintext %q in a field", secret)
	}
}

func reflectValueContainsBytes(value reflect.Value, needle []byte) bool {
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return false
		}
		return reflectValueContainsBytes(value.Elem(), needle)
	case reflect.String:
		return strings.Contains(value.String(), string(needle))
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			bytes := make([]byte, value.Len())
			for i := range bytes {
				bytes[i] = byte(value.Index(i).Uint())
			}
			return strings.Contains(string(bytes), string(needle))
		}
		for i := range value.Len() {
			if reflectValueContainsBytes(value.Index(i), needle) {
				return true
			}
		}
	case reflect.Struct:
		for _, field := range value.Fields() {
			if reflectValueContainsBytes(field, needle) {
				return true
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return false
		}
		iter := value.MapRange()
		for iter.Next() {
			if reflectValueContainsBytes(iter.Key(), needle) || reflectValueContainsBytes(iter.Value(), needle) {
				return true
			}
		}
	}
	return false
}
