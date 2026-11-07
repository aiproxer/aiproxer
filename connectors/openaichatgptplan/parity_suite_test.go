// Parity and boundary proofs for the openai-chatgpt-plan connector.
//
// Scope: the connector boundary - discovery, configuration, posture
// declaration and explicit failures. The account-scoped inventory and the
// stream-only Responses execution are proven in internal/service, next to the
// cache and the transport; this file keeps the host-facing failure semantics
// of an unavailable inventory and of an instance that has no registration
// store to authorize an attempt.
package openaichatgptplan_test

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// memStream is a minimal in-memory ExecuteStream for the connector's own
// execute proof.
type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }

func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

func configure(t *testing.T, kind string, cfgYAML string) backendplugin.ConfiguredInstance {
	t.Helper()
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: kind,
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("Configure(%q): %v", kind, err)
	}
	return inst
}

// TestDescribe_DirectInferenceFactory pins the exported factory table.
//
// The direct export is the only export of this connector: the SIWC-backed
// Codex app-server factory belongs to a later phase and must not be
// selectable yet. The posture is user-scoped OAuth over a local-only scope.
//
// The declared capability surface is the surface SIWC supports and this
// connector provides. It is a declaration, not a proof of inference: the
// execution proof below covers the honest failure of an instance that has no
// registration store, and internal/service proves the streaming path itself.
func TestDescribe_DirectInferenceFactory(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != service.PluginID {
		t.Fatalf("plugin_id %q want %q", d.PluginID, service.PluginID)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("descriptor validation: %v", err)
	}
	if len(d.Factories) != 1 {
		t.Fatalf("factories=%d want 1: %+v", len(d.Factories), d.Factories)
	}
	fac := d.Factories[0]
	if fac.Kind != service.FactoryKind {
		t.Fatalf("kind %q want %q", fac.Kind, service.FactoryKind)
	}
	if fac.CredentialMode != backendplugin.CredentialModeOAuthUser {
		t.Errorf("credential_mode %q want %q", fac.CredentialMode, backendplugin.CredentialModeOAuthUser)
	}
	if fac.AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Errorf("access_scope %q want %q", fac.AccessScope, backendplugin.AccessScopeLocalOnly)
	}
	if fac.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Errorf("process_sharing %q want %q", fac.ProcessSharing, backendplugin.ProcessSharingPerInstance)
	}
	if !fac.SupportsDynamicInventory {
		t.Error("direct export must advertise dynamic inventory")
	}
	if !fac.StaticCapabilities.Streaming {
		t.Error("direct export must advertise streaming")
	}
	if !fac.StaticCapabilities.Tools {
		t.Error("direct export must advertise tool support")
	}
	if fac.StaticCapabilities.ReasoningReplay || fac.StaticCapabilities.Compaction {
		t.Errorf("direct export must not advertise capabilities it cannot honor: %+v", fac.StaticCapabilities)
	}
	if fac.SupportsCountTokens || fac.SupportsFinalizeBilling {
		t.Errorf("direct export must not advertise accounting capabilities it does not implement: %+v", fac)
	}
	if len(fac.RoutePrefixes) != 1 || fac.RoutePrefixes[0] != service.FactoryKind {
		t.Errorf("route prefixes %v want [%q]", fac.RoutePrefixes, service.FactoryKind)
	}
	// The descriptor must not advertise optional ABI features this build
	// cannot exercise. The cancellation handshake is provided by the shared
	// execute helper this connector runs, and the accounting evidence sideband
	// is bridged once per completed stream by the managed stream. Ordered
	// items and proxy-owned session authority remain unadvertised: promising
	// them would make the host send content this build drops.
	//
	// semantic_extensions_v1 is exercised narrowly: it is the bounded ABI
	// carrier that lets the preview policy receive a client prompt-cache hint
	// and omit it explicitly (or refuse it in strict posture). The descriptor
	// must NOT advertise exact_openresponses_fields instead: that feature
	// promises exact field semantics, including exact reasoning/file/extension
	// fields, which the public SIWC preview route does not accept.
	exercisedFeatures := map[string]bool{
		backendplugin.FeatureCancellationHandshake: true,
		backendplugin.FeatureAccountingEvidence:    true,
		backendplugin.FeatureSemanticExtensions:    true,
	}
	advertised := map[string]bool{}
	for _, f := range d.Features {
		advertised[f.Name] = true
		if !exercisedFeatures[f.Name] {
			t.Errorf("descriptor advertises feature %q that this build does not exercise", f.Name)
		}
	}
	if !advertised[backendplugin.FeatureSemanticExtensions] {
		t.Errorf("descriptor must advertise %q so the prompt-cache alias reaches the preview policy, got %+v",
			backendplugin.FeatureSemanticExtensions, d.Features)
	}
	if advertised[backendplugin.FeatureExactOpenResponsesFields] {
		t.Errorf("descriptor must not advertise %q: the SIWC preview route does not accept exact OpenResponses fields",
			backendplugin.FeatureExactOpenResponsesFields)
	}
}

// TestConfigure_AcceptsDirectFactoryKind proves the host can configure the
// direct export and that the configured instance answers the profile the
// descriptor advertised.
//
// The store path is built from the test's own temporary directory, so it is
// absolute on every platform the connector supports: a hardcoded Unix path
// is not absolute on Windows, where the configuration boundary requires a
// volume or a UNC prefix.
func TestConfigure_AcceptsDirectFactoryKind(t *testing.T) {
	t.Parallel()
	storePath := filepath.Join(t.TempDir(), "siwc-store")
	inst := configure(t, service.FactoryKind, "profile_store_path: "+yamlScalar(storePath)+"\n")
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.Streaming || !profile.Capabilities.Tools {
		t.Errorf("resolved profile must match the advertised capabilities: %+v", profile.Capabilities)
	}
	if !profile.SupportsDynamicInventory {
		t.Error("resolved profile must report dynamic inventory")
	}
	// Context compaction stays outside this provider adapter (spec task 8.3;
	// requirements 10.1, 10.3 and 10.7). Generic proxy-owned compaction may
	// reduce canonical context where its semantics are valid, but this
	// connector has no public compaction carrier and never calls the retired
	// private Codex /responses/compact endpoint. Advertising compaction or its
	// codex.responses.compaction.v2 dialect would route a compact operation
	// into a post-admission refusal instead of an honest capability miss.
	if profile.Capabilities.Compaction {
		t.Error("the resolved profile advertises compaction; the SIWC connector has no public compaction route")
	}
	if len(profile.DialectSupport.CompactionDialects) != 0 {
		t.Errorf("the resolved profile advertises compaction dialects, including the retired private Codex dialect: %+v",
			profile.DialectSupport.CompactionDialects)
	}
	if err := inst.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestConfigure_RejectsUnknownFactoryKind proves Configure validates the
// factory kind instead of silently serving a request for a kind this
// connector does not export.
func TestConfigure_RejectsUnknownFactoryKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"openai-chatgpt-plan-app-server", "openai-codex", ""} {
		_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
			InstanceID:  "test-instance",
			FactoryKind: kind,
		})
		if err == nil {
			t.Fatalf("Configure(%q) must fail for a kind this connector does not export", kind)
		}
		if !strings.Contains(err.Error(), service.FactoryKind) {
			t.Errorf("Configure(%q) error %q must name the exported kind %q", kind, err, service.FactoryKind)
		}
	}
}

// TestConfigure_RejectsLiteralCredentialsInConfig proves the connector never
// accepts credential material in its configuration subtree: the effective
// credential is the selected user's SIWC profile store.
func TestConfigure_RejectsLiteralCredentialsInConfig(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("access_token: literal-token\n"),
	})
	if err == nil {
		t.Fatal("Configure must reject a literal credential in the configuration subtree")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error %q must state that literal credentials are forbidden", err)
	}
}

// TestConfigure_RejectsLiteralCredentialAliases proves the forbidden-key
// filter covers the whole named list, not only one spelling: every listed
// credential name, and the case/hyphen spellings the decoder normalizes to it,
// is rejected with the explicit forbidden diagnostic, and the rejected value
// never appears in the error. The list is the connector's own, so a regression
// that drops entries must fail here.
func TestConfigure_RejectsLiteralCredentialAliases(t *testing.T) {
	t.Parallel()
	// Every entry of the connector's forbidden list, plus the normalization
	// forms the decoder applies to a key (lower case, hyphen to underscore).
	// The assertion is on the shared decoder path, not on one spelling.
	for _, key := range []string{
		"access_token", "refresh_token", "id_token", "api_key", "apikey",
		"token", "secret", "client_secret", "pat", "authorization_code",
		"code_verifier",
		"ACCESS-TOKEN", "REFRESH-TOKEN", "API-KEY", "CLIENT-SECRET",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			assertLiteralCredentialRejected(t, key, true)
		})
	}
}

// TestConfigure_RejectsCamelCaseCredentialKey proves a credential name that
// the decoder does not normalize into the forbidden list is still rejected,
// and is still rejected without echoing the value.
//
// The strict decoder reports such a key as an unknown configuration field
// rather than as a named credential. That is a different diagnostic, not a
// weaker guarantee: the key is refused either way and the credential value
// never reaches the error.
func TestConfigure_RejectsCamelCaseCredentialKey(t *testing.T) {
	t.Parallel()
	assertLiteralCredentialRejected(t, "refreshToken", false)
}

// assertLiteralCredentialRejected configures the factory with a literal
// credential under key and asserts the configuration is refused. When
// wantForbiddenDiagnostic is set, the refusal must also carry the specific
// forbidden-key diagnostic.
func assertLiteralCredentialRejected(t *testing.T, key string, wantForbiddenDiagnostic bool) {
	t.Helper()
	const secret = "literal-secret-value"
	_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(key + ": " + secret + "\n"),
	})
	if err == nil {
		t.Fatalf("Configure must reject the literal credential key %q", key)
	}
	if wantForbiddenDiagnostic && !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error %q must state that literal credentials are forbidden", err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("error %q must name the rejected key %q", err, key)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q must not echo the rejected credential value", err)
	}
}

// TestConfigure_RejectsUnknownConfigurationKey proves the configuration
// subtree is closed: an unrecognized key is a configuration error, never a
// silently ignored value that could carry credential material under a name
// this connector does not know.
func TestConfigure_RejectsUnknownConfigurationKey(t *testing.T) {
	t.Parallel()
	storePath := filepath.Join(t.TempDir(), "siwc-store")
	_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("profile_store_path: " + yamlScalar(storePath) + "\nunknown_key: value\n"),
	})
	if err == nil {
		t.Fatal("Configure must reject an unknown configuration key")
	}
	if !strings.Contains(err.Error(), "unknown_key") {
		t.Errorf("error %q must name the rejected key", err)
	}
}

// TestParseConfigYAML_AcceptsTheHostRecordPath proves the configuration
// contract names the host record store.
//
// The host record is a separate protected file from the profile store, and
// it is referenceable the same way: local state is addressed by path, and
// the connector must not decide the location of the host identity behind
// the operator's back. An empty value keeps the per-installation default,
// which is resolved when the store is created.
//
// The paths are built from the test's own temporary directory, so they are
// absolute on every platform the connector supports: a hardcoded Unix path
// is not absolute on Windows, where the boundary requires a volume or a UNC
// prefix.
func TestParseConfigYAML_AcceptsTheHostRecordPath(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "siwc-store.json")
	hostPath := filepath.Join(t.TempDir(), "siwc-host.json")
	cfg, err := service.ParseConfigYAML([]byte(
		"profile_store_path: " + yamlScalar(storePath) + "\nhost_record_path: " + yamlScalar(hostPath) + "\n",
	))
	if err != nil {
		t.Fatalf("ParseConfigYAML: %v", err)
	}
	if cfg.HostRecordPath != hostPath {
		t.Errorf("host_record_path = %q, want the trimmed configured path %q", cfg.HostRecordPath, hostPath)
	}
	if cfg.ProfileStorePath != storePath {
		t.Errorf("profile_store_path = %q, want the trimmed configured path %q", cfg.ProfileStorePath, storePath)
	}

	// The configured value is normalized before it is checked, so a padded
	// value still resolves to the same absolute path.
	padded, err := service.ParseConfigYAML([]byte("host_record_path: " + yamlScalar("  "+hostPath+"  ") + "\n"))
	if err != nil {
		t.Fatalf("ParseConfigYAML (padded): %v", err)
	}
	if padded.HostRecordPath != hostPath {
		t.Errorf("host_record_path = %q, want the trimmed configured path %q", padded.HostRecordPath, hostPath)
	}

	defaults, err := service.ParseConfigYAML(nil)
	if err != nil {
		t.Fatalf("ParseConfigYAML(nil): %v", err)
	}
	if defaults.HostRecordPath != "" {
		t.Errorf("host_record_path = %q, want the empty value that selects the default location", defaults.HostRecordPath)
	}
}

// yamlScalar renders a filesystem path as a YAML single-quoted scalar.
//
// Single quotes keep backslashes literal, which a Windows path needs, and
// an embedded quote is doubled.
func yamlScalar(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// TestParseConfigYAML_RejectsARelativeProfileStorePath proves the
// configuration boundary refuses a relative profile store path.
//
// A relative path would be resolved against the working directory of
// whichever process started the connector, so the same configuration would
// address a different directory after a restart with a different working
// directory - a silent change of which registrations the connector can see.
// Resolving it against the current working directory would not fix that, so
// the path is refused.
func TestParseConfigYAML_RejectsARelativeProfileStorePath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"profiles", "state/profiles", "./profiles"} {
		_, err := service.ParseConfigYAML([]byte("profile_store_path: " + path + "\n"))
		if err == nil {
			t.Errorf("ParseConfigYAML must refuse the relative profile_store_path %q", path)
			continue
		}
		if !strings.Contains(err.Error(), "absolute") {
			t.Errorf("error %q must state that the profile store path must be absolute", err)
		}
	}
}

// TestParseConfigYAML_RejectsARelativeHostRecordPath proves the
// configuration boundary refuses a relative host record path.
//
// A relative path would be resolved against the working directory of
// whichever process started the connector, so the same configuration would
// address a different file after a restart with a different working
// directory - a silent change of the host identity. Resolving it against the
// current working directory would not fix that, so the path is refused.
func TestParseConfigYAML_RejectsARelativeHostRecordPath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"host.json", "state/host.json", "./host.json"} {
		_, err := service.ParseConfigYAML([]byte("host_record_path: " + path + "\n"))
		if err == nil {
			t.Errorf("ParseConfigYAML must refuse the relative host_record_path %q", path)
			continue
		}
		if !strings.Contains(err.Error(), "absolute") {
			t.Errorf("error %q must state that the host record path must be absolute", err)
		}
	}
}

// TestParseConfigYAML_RejectsAHostRecordPathThatDoesNotNameAFile proves the
// configuration boundary refuses a host record path that does not name a
// file.
//
// The host record is a file. A path whose last component is empty, "." or
// ".." names a directory, and the store that owns the record would refuse
// it later. The configuration boundary refuses it at parse time instead, so
// the operator learns about it from the configuration error.
//
// The paths are built from the test's own temporary directory, so they are
// absolute on every platform the connector supports.
func TestParseConfigYAML_RejectsAHostRecordPathThatDoesNotNameAFile(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "state")
	for _, path := range []string{
		base + "/",
		base + "/.",
		base + "/..",
	} {
		_, err := service.ParseConfigYAML([]byte("host_record_path: " + yamlScalar(path) + "\n"))
		if err == nil {
			t.Errorf("ParseConfigYAML must refuse the host_record_path %q", path)
			continue
		}
		if !strings.Contains(err.Error(), "name a file") {
			t.Errorf("error %q must state that the host record path must name a file", err)
		}
	}
}

// TestConfigure_RejectsHostDeliveredSecret proves a host-delivered secret is
// refused instead of silently ignored: this export declares no credential
// contract, and its effective credential is the selected user's SIWC profile.
func TestConfigure_RejectsHostDeliveredSecret(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: service.FactoryKind,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("host-token")}},
	})
	if err == nil {
		t.Fatal("Configure must reject a host-delivered secret for a credential-free export")
	}
	if !strings.Contains(err.Error(), "no host-delivered credential") {
		t.Errorf("error %q must state that the export carries no host-delivered credential", err)
	}
}

// TestExecute_FailsExplicitlyWithoutRegistrationStore proves the attempt path
// is honest when the instance cannot authorize anything: it terminates with
// exactly one explicit failure instead of a fabricated stream, a duplicated
// terminal, or a stream that ends without a verdict.
//
// An instance without a registration store has no account whose credential
// could authorize the attempt, so the refusal happens before any provider I/O.
func TestExecute_FailsExplicitlyWithoutRegistrationStore(t *testing.T) {
	t.Parallel()
	inst := configure(t, service.FactoryKind, "")
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "al-1",
		BLegID:           "bl-1",
		CanonicalModelID: service.FactoryKind + "/some-model",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind:       backendplugin.ClientFrameStart,
		InstanceID: "test-instance",
		Invocation: &inv,
	}}}
	err := inst.Execute(ms)
	if !errors.Is(err, service.ErrExecutionUnavailable) {
		t.Fatalf("Execute error %v want %v", err, service.ErrExecutionUnavailable)
	}
	var terminal *backendplugin.Terminal
	terminals := 0
	events := 0
	for _, frame := range ms.outbox {
		switch frame.Kind {
		case backendplugin.ServerFrameTerminal:
			terminals++
			terminal = frame.Terminal
		case backendplugin.ServerFrameEvent:
			events++
		}
	}
	if terminals != 1 {
		t.Fatalf("Execute must emit exactly one terminal frame, got %d: %+v", terminals, ms.outbox)
	}
	if terminal == nil {
		t.Fatal("Execute must terminate the attempt with a terminal frame")
	}
	if terminal.Status != backendplugin.TerminalFailure {
		t.Errorf("terminal status %q want %q", terminal.Status, backendplugin.TerminalFailure)
	}
	if terminal.Error == nil {
		t.Error("the failure terminal must carry a plugin error")
	} else if terminal.Error.Retryable {
		t.Error("the unusable instance must not be advertised as retryable")
	}
	if events != 0 {
		t.Errorf("Execute emitted %d canonical events; an unusable instance must not fabricate output", events)
	}
}

// TestInventory_UnavailableIsNotASuccessfulEmptyCatalog pins the host-facing
// semantics of an unavailable inventory in BOTH root configurations: an
// instance with no store at all, and an instance whose store holds no selected
// registration.
//
// The host turns a ListModels error into discovery metadata with
// modelinventory.DiscoveryFromLoadError, and a successful zero-model response
// into modelinventory.DiscoveryFromSnapshot. The unavailable inventory must
// land in the first bucket: a successful empty response reaches the operator as
// a fetched catalog that found no models, which is the hidden degradation this
// must avoid.
func TestInventory_UnavailableIsNotASuccessfulEmptyCatalog(t *testing.T) {
	t.Parallel()
	storePath := filepath.Join(t.TempDir(), "siwc-store")
	configurations := []struct {
		name   string
		config string
	}{
		{name: "no-store", config: ""},
		{
			name:   "configured-store-with-no-selection",
			config: "profile_store_path: " + yamlScalar(storePath) + "\n",
		},
	}
	for _, configuration := range configurations {
		t.Run(configuration.name, func(t *testing.T) {
			t.Parallel()
			inst := configure(t, service.FactoryKind, configuration.config)
			resp, err := inst.ListModels(context.Background(), 10)
			if err == nil {
				t.Fatalf("ListModels must fail while the account-scoped inventory is unavailable; got a successful response with %d models and source %q", len(resp.Models), resp.InventorySource)
			}
			if !modelinventory.IsOperational(err) {
				t.Errorf("the unavailable inventory error must carry the connector-local operational marker: %v", err)
			}
			disc := modelinventory.DiscoveryFromLoadError(err)
			if disc.Status == modelinventory.DiscoveryStatusEmpty {
				t.Fatalf("an unavailable inventory must never be observable as an empty catalog (discovery status %q)", disc.Status)
			}
			if disc.Status != modelinventory.DiscoveryStatusUnavailable {
				t.Errorf("discovery status %q want %q", disc.Status, modelinventory.DiscoveryStatusUnavailable)
			}
			if disc.ErrorCode != modelinventory.ErrorCodeUnavailable {
				t.Errorf("discovery error code %q want %q", disc.ErrorCode, modelinventory.ErrorCodeUnavailable)
			}
		})
	}
	// Contrast: the same zero-model result reported as a success is exactly
	// the "empty" discovery the host would show for an account without
	// models. The unavailable path must not land there.
	empty := modelinventory.DiscoveryFromSnapshot(modelinventory.Snapshot{})
	if empty.Status != modelinventory.DiscoveryStatusEmpty {
		t.Fatalf("host inventory contract changed: a zero-model snapshot is %q, want %q", empty.Status, modelinventory.DiscoveryStatusEmpty)
	}
}

// TestConformance_BoundaryNegotiateDescribeConfigureClose covers the
// backend-plugin ABI boundary this task owns: negotiate, describe,
// configure, resolve and close against the real service.
//
// The shared conformance suite (pkg/aipsdk/backendplugin/conformance) asserts
// the same boundary plus two cases this build cannot satisfy end to end: its
// inventory cases need a configured registration with a selected profile, and
// its execute case needs a successful streaming attempt. The account-scoped
// inventory and the streaming execution are exercised in internal/service with
// a real store, client and local reference server, so this boundary proof keeps
// to discovery/describe/configure/close and the explicit unavailable states.
func TestConformance_BoundaryNegotiateDescribeConfigureClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := service.New()
	desc, err := svc.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := desc.Validate(); err != nil {
		t.Fatalf("descriptor validation: %v", err)
	}
	host := backendplugin.ProtocolOffer{
		Major:                   backendplugin.ProtocolMajorV1,
		Minor:                   backendplugin.ProtocolMinorAccountingEvidenceV2,
		DisableTransportRetries: true,
		Features:                desc.Features,
	}
	plugin := backendplugin.ProtocolOffer{
		Major:                   desc.ProtocolMajor,
		Minor:                   desc.ProtocolMinor,
		DisableTransportRetries: true,
		Features:                desc.Features,
	}
	neg, err := backendplugin.Negotiate(host, plugin)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if !neg.Compatible {
		t.Fatalf("negotiation incompatible: %s", neg.RejectReason)
	}
	if err := backendplugin.MustNegotiateBeforeConfigure(neg); err != nil {
		t.Fatalf("negotiate before configure: %v", err)
	}
	inst, err := svc.Configure(ctx, backendplugin.ConfigureRequest{
		InstanceID:  "c-boundary",
		FactoryKind: service.FactoryKind,
		Negotiation: neg,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	profile, err := inst.Resolve(ctx, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	fac := desc.Factories[0]
	if profile.Capabilities.Streaming != fac.StaticCapabilities.Streaming {
		t.Errorf("resolved streaming %v != declared %v", profile.Capabilities.Streaming, fac.StaticCapabilities.Streaming)
	}
	if profile.Capabilities.Tools != fac.StaticCapabilities.Tools {
		t.Errorf("resolved tools %v != declared %v", profile.Capabilities.Tools, fac.StaticCapabilities.Tools)
	}
	if profile.SupportsDynamicInventory != fac.SupportsDynamicInventory {
		t.Errorf("resolved dynamic inventory %v != declared %v", profile.SupportsDynamicInventory, fac.SupportsDynamicInventory)
	}
	if profile.SupportsCountTokens != fac.SupportsCountTokens {
		t.Errorf("resolved count_tokens %v != declared %v", profile.SupportsCountTokens, fac.SupportsCountTokens)
	}
	if profile.SupportsFinalizeBilling != fac.SupportsFinalizeBilling {
		t.Errorf("resolved finalize_billing %v != declared %v", profile.SupportsFinalizeBilling, fac.SupportsFinalizeBilling)
	}
	if len(profile.RoutePrefixes) != 1 || profile.RoutePrefixes[0] != service.FactoryKind {
		t.Errorf("resolved route prefixes %v want [%q]", profile.RoutePrefixes, service.FactoryKind)
	}
	if err := inst.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// moduleRoot returns the connector module root (the directory holding its
// go.mod).
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("connector module go.mod not found")
		}
		dir = parent
	}
}

// TestParity_ConnectorImportsNoHostInternals proves the connector reaches the
// host only through the public ABI: it must not import the root module's
// internal packages, the legacy private-Codex connector, or any other
// module's internal packages.
func TestParity_ConnectorImportsNoHostInternals(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	const selfPrefix = "github.com/aiproxer/aiproxer/connectors/openaichatgptplan"
	forbidden := []string{
		"github.com/aiproxer/aiproxer/internal/",
		"github.com/aiproxer/aiproxer/connectors/codex/",
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, imp := range file.Imports {
			pathLit := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.HasPrefix(pathLit, bad) {
					t.Errorf("%s imports forbidden package %s", path, pathLit)
				}
			}
			if strings.Contains(pathLit, "/internal/") && !strings.HasPrefix(pathLit, selfPrefix) {
				t.Errorf("%s imports another module's internal package %s", path, pathLit)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
