package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// Fail-closed and mutation proofs for the first-party multi-user census.
//
// These drive [collectFirstPartyCensus] against real on-disk release descriptors
// and manifest templates, so a brand-new connector is enumerated exactly the way a
// live connector is: no hand-built export map, no bypass of the discovery walk.

// writeReleaseConnector materializes one `connectors/<name>/` tree with a release
// descriptor and the manifest template it points at, and returns the connector
// directory.
func writeReleaseConnector(t *testing.T, connectorsRoot, name string, release, manifest string) {
	t.Helper()
	dir := filepath.Join(connectorsRoot, name)
	if err := os.MkdirAll(filepath.Join(dir, "manifest"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.yaml"), []byte(release), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest", "template.backendplugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseYAML(pluginID, factoryKind string) string {
	return "schema: " + connectorReleaseSchema + "\n" +
		"plugin_id: " + pluginID + "\n" +
		"factory_kind: " + factoryKind + "\n" +
		"module: example.invalid/connectors/x\n" +
		"command: ./cmd/x\n" +
		"manifest_template: manifest/template.backendplugin.json\n" +
		"version: 0.1.0\n" +
		"build_id: fixture\n" +
		"profiles:\n  - full\n"
}

func manifestJSON(pluginID, kind, credentialMode, accessScope, executionClass string) string {
	return `{
  "schema": "` + backendManifestSchema + `",
  "plugin_id": "` + pluginID + `",
  "version": "0.1.0",
  "build_id": "REPLACE_BUILD_ID",
  "executable": "bin/x",
  "sha256": "REPLACE_SHA256",
  "protocol_major": 1,
  "protocol_min_minor": 0,
  "protocol_max_minor": 0,
  "platforms": [{"os": "linux", "arch": "amd64"}],
  "exports": [
    {
      "kind": "` + kind + `",
      "credential_mode": "` + credentialMode + `",
      "access_scope": "` + accessScope + `",
      "process_sharing": "per_instance",
      "execution_class": "` + executionClass + `"
    }
  ]
}
`
}

// TestFirstPartyCensus_NewReleaseIsEnumeratedAndDeniedByDefault is the core
// fail-closed proof on real files: a brand-new connector that declares itself
// maximally permissive — including a new ACP-shaped runtime that misclassifies
// itself as plain inference — is enumerated by the census and still denied
// multi-user eligibility because it is absent from the host approval registry.
func TestFirstPartyCensus_NewReleaseIsEnumeratedAndDeniedByDefault(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	root := t.TempDir()
	pluginID, kind := "com.aiproxer.backend.newlyacp", "newlyacp"
	writeReleaseConnector(t, root, "newlyacp",
		releaseYAML(pluginID, kind),
		manifestJSON(pluginID, kind, "static", scopeAny, classInference))

	census, err := collectFirstPartyCensus(root)
	if err != nil {
		t.Fatalf("census over the fixture connector tree: %v", err)
	}
	if len(census.Escapes) != 0 {
		t.Fatalf("fixture connector escapes: %v", census.Escapes)
	}
	indexed, err := census.byKind()
	if err != nil {
		t.Fatal(err)
	}
	export, ok := indexed[kind]
	if !ok {
		t.Fatalf("census does not see the new export %q; every first-party release must be enumerated", kind)
	}
	// The metadata itself is internally consistent, so no posture rule catches it:
	// default deny is the only thing standing between it and a shared deployment.
	if reason := firstPartyPostureViolation(export, personalAuthExportKinds(host)); reason != "" {
		t.Fatalf("fixture must be self-consistent metadata so default deny is what rejects it, got: %s", reason)
	}
	if _, approved := host.Approvals[kind]; approved {
		t.Fatalf("new factory %q must not be present in the host approval registry", kind)
	}
	if hostAllowsMultiUser(host, export) {
		t.Fatalf("new factory %q is multi-user eligible without a host approval; absence from the registry must deny", kind)
	}
}

// TestFirstPartyCensus_ReleaseMutationsGoRed proves the census discovery walk
// fails closed on each structural hole, and the positive control proves a healthy
// fixture tree is accepted.
func TestFirstPartyCensus_ReleaseMutationsGoRed(t *testing.T) {
	t.Parallel()
	pluginID, kind := "com.aiproxer.backend.fx", "fx"
	healthy := manifestJSON(pluginID, kind, "static", scopeAny, classInference)

	cases := []struct {
		name       string
		release    string
		manifest   string
		writeOnly  bool
		wantEscape string
	}{
		{name: "healthy control", release: releaseYAML(pluginID, kind), manifest: healthy},
		{
			name:       "missing manifest template",
			release:    releaseYAML(pluginID, kind),
			writeOnly:  true,
			wantEscape: "unreadable",
		},
		{
			name:       "release without manifest_template",
			release:    "schema: " + connectorReleaseSchema + "\nplugin_id: " + pluginID + "\nfactory_kind: " + kind + "\n",
			manifest:   healthy,
			wantEscape: "declares no manifest_template",
		},
		{
			name: "manifest_template escaping the module",
			release: "schema: " + connectorReleaseSchema + "\nplugin_id: " + pluginID + "\nfactory_kind: " + kind +
				"\nmanifest_template: ../../escape/template.backendplugin.json\n",
			manifest:   healthy,
			wantEscape: "escapes its connector module",
		},
		{
			name:       "factory_kind not declared by any export",
			release:    releaseYAML(pluginID, "other-kind"),
			manifest:   healthy,
			wantEscape: "is declared by no export",
		},
		{
			name:       "plugin_id mismatch between release and manifest",
			release:    releaseYAML("com.aiproxer.backend.other", kind),
			manifest:   healthy,
			wantEscape: "does not match release.yaml plugin_id",
		},
		{
			name: "wrong release schema",
			release: "schema: " + backendManifestSchema + "\nplugin_id: " + pluginID + "\nfactory_kind: " + kind +
				"\nmanifest_template: manifest/template.backendplugin.json\n",
			manifest:   healthy,
			wantEscape: "is not",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dir := filepath.Join(root, "fx")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "release.yaml"), []byte(tc.release), 0o600); err != nil {
				t.Fatal(err)
			}
			if !tc.writeOnly {
				if err := os.MkdirAll(filepath.Join(dir, "manifest"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "manifest", "template.backendplugin.json"), []byte(tc.manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			census, err := collectFirstPartyCensus(root)
			if err != nil {
				t.Fatalf("census: %v", err)
			}
			if tc.wantEscape == "" {
				if len(census.Escapes) != 0 {
					t.Fatalf("healthy fixture must not escape the census, got %v", census.Escapes)
				}
				if len(census.Exports) != 1 {
					t.Fatalf("healthy fixture must yield exactly one export, got %d", len(census.Exports))
				}
				return
			}
			if !slicesContainsFold(census.Escapes, tc.wantEscape) {
				t.Fatalf("census escapes %v do not mention %q", census.Escapes, tc.wantEscape)
			}
		})
	}
}

// TestHostAllowsMultiUser_NegativePostures proves an approval can never widen a
// non-multi-user posture: even a factory the host approved is denied when its
// packaged manifest says local-only, agent-runtime, user-OAuth, or undeclared.
func TestHostAllowsMultiUser_NegativePostures(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	kind := "approvedbutlocalonly"
	// A synthetic approval is unavoidable here: the guard under test is the
	// interaction between an approval and an incompatible posture, and the live
	// registry is (correctly) free of such a row.
	host.Approvals[kind] = standardplugins.MultiUserBackendApproval{
		FactoryKind:       kind,
		CredentialClass:   standardplugins.MultiUserCredentialOperatorStatic,
		ExecutionLocation: standardplugins.MultiUserExecutionRemoteProvider,
		Reference:         "test fixture: approved factory with an incompatible manifest posture",
	}
	host.ApprovedKinds = append(host.ApprovedKinds, kind)
	slices.Sort(host.ApprovedKinds)

	personalAuth := personalAuthExportKinds(host)
	cases := []struct {
		name    string
		export  firstPartyExport
		wantMsg string
	}{
		{
			name:    "approved local_only",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: "static", AccessScope: scopeLocalOnly, ExecutionClass: classInference},
			wantMsg: "access_scope",
		},
		{
			name:    "approved agent_runtime",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: "static", AccessScope: scopeLocalOnly, ExecutionClass: classAgentRuntime},
			wantMsg: "execution_class",
		},
		{
			name:    "approved oauth_user",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: credentialOAuthUser, AccessScope: scopeLocalOnly, ExecutionClass: classInference},
			wantMsg: "credential_mode",
		},
		{
			name:    "approved unknown credential",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: credentialUnknown, AccessScope: scopeAny, ExecutionClass: classInference},
			wantMsg: "credential_mode",
		},
		{
			name:    "approved empty credential",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, AccessScope: scopeAny, ExecutionClass: classInference},
			wantMsg: "credential_mode",
		},
		{
			name:    "approved unknown access scope",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: "static", AccessScope: "shared_safe", ExecutionClass: classInference},
			wantMsg: "access_scope",
		},
		{
			name:    "approved unknown execution class",
			export:  firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: "static", AccessScope: scopeAny, ExecutionClass: "totally_safe"},
			wantMsg: "execution_class",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := approvedPostureViolation(tc.export, personalAuth)
			if reason == "" {
				t.Fatalf("approved posture %+v must be reported incompatible", tc.export)
			}
			if !strings.Contains(reason, tc.wantMsg) {
				t.Fatalf("violation %q must name the regressed field %q", reason, tc.wantMsg)
			}
			if hostAllowsMultiUser(host, tc.export) {
				t.Fatalf("an approval must never widen posture %+v into multi-user eligibility", tc.export)
			}
		})
	}

	// Positive control: the same synthetic approval with a compatible posture passes.
	ok := firstPartyExport{Connector: "connectors/fixture", Kind: kind, CredentialMode: "static", AccessScope: scopeAny, ExecutionClass: classInference}
	if reason := approvedPostureViolation(ok, personalAuth); reason != "" {
		t.Fatalf("compatible approved posture must pass, got %s", reason)
	}
	if !hostAllowsMultiUser(host, ok) {
		t.Fatal("an explicitly approved, compatible factory must remain multi-user eligible")
	}
}

// TestFirstPartyCensus_PostureMutationFixturesGoRed proves each frozen invariant
// fails closed, using the same detectors the live census uses.
func TestFirstPartyCensus_PostureMutationFixturesGoRed(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	personalAuth := personalAuthExportKinds(host)
	cases := []struct {
		fixture  string
		contains string
	}{
		{"agent_runtime_access_any.json", "execution_class"},
		{"oauth_user_access_any.json", "credential_mode"},
		{"personal_auth_access_any.json", "personal-auth"},
		{"unknown_access_scope.json", "access_scope"},
		{"unknown_execution_class.json", "execution_class"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			export := readExportFixture(t, tc.fixture)
			reason := firstPartyPostureViolation(export, personalAuth)
			if reason == "" {
				t.Fatalf("fixture %s (%s) must be rejected but passed the census posture check", tc.fixture, export.Ref())
			}
			if !strings.Contains(reason, tc.contains) {
				t.Fatalf("violation %q must mention %q so the failure names the regressed field", reason, tc.contains)
			}
			if hostAllowsMultiUser(host, export) {
				t.Fatalf("regressed export %s must never be multi-user eligible", export.Ref())
			}
		})
	}
}

// TestFirstPartyCensus_StaleApprovalFixtureDetected proves a stale approval row
// (a factory that was renamed or removed) is reported instead of silently ignored.
func TestFirstPartyCensus_StaleApprovalFixtureDetected(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(multiUserPolicyFixtureDir), "stale_approval_kind.txt"))
	if err != nil {
		t.Fatal(err)
	}
	kind := strings.TrimSpace(string(raw))
	if kind == "" {
		t.Fatal("stale approval fixture is empty")
	}
	if _, exists := census[kind]; exists {
		t.Fatalf("stale approval fixture %q must not exist in the live census", kind)
	}
	if standardplugins.IsEssentialBackendKind(kind) {
		t.Fatalf("stale approval fixture %q must not be an essential built-in", kind)
	}
	host.Approvals[kind] = standardplugins.MultiUserBackendApproval{
		FactoryKind:       kind,
		CredentialClass:   standardplugins.MultiUserCredentialOperatorStatic,
		ExecutionLocation: standardplugins.MultiUserExecutionRemoteProvider,
		Reference:         "test fixture: approval for a factory that no longer exists",
	}
	host.ApprovedKinds = append(host.ApprovedKinds, kind)
	if got := staleApprovals(host, census); !slicesContains(got, kind) {
		t.Fatalf("stale approval %q not detected, got %v", kind, got)
	}
}

// TestFirstPartyCensus_EssentialApprovalsAreNotStale proves the built-in/connector
// split uses the actual catalog: an essential approval is not reported as a stale
// connector factory, and a non-essential, non-connector approval is.
func TestFirstPartyCensus_EssentialApprovalsAreNotStale(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	essential := slices.Sorted(slices.Values(host.EssentialKinds))
	if len(essential) == 0 {
		t.Fatal("essential built-in catalog is empty")
	}
	if got := staleApprovals(host, census); len(got) != 0 {
		t.Fatalf("live registry must have no stale connector approval, got %v", got)
	}
	for _, kind := range essential {
		if !host.isConnectorApproval(kind) {
			continue
		}
		t.Errorf("essential built-in %q must be treated as out of connector census scope", kind)
	}

	// A non-essential approval that is not a connector export is unverifiable and
	// must be reported rather than assumed valid.
	orphan := "orphan-factory-kind"
	host.Approvals[orphan] = standardplugins.MultiUserBackendApproval{
		FactoryKind:       orphan,
		CredentialClass:   standardplugins.MultiUserCredentialOperatorStatic,
		ExecutionLocation: standardplugins.MultiUserExecutionRemoteProvider,
		Reference:         "test fixture: approval for an unknown factory",
	}
	host.ApprovedKinds = append(host.ApprovedKinds, orphan)
	if got := staleApprovals(host, census); !slicesContains(got, orphan) {
		t.Fatalf("orphan approval must be reported stale, got %v", got)
	}
}

func slicesContains(values []string, want string) bool {
	return slices.Contains(values, want)
}

func slicesContainsFold(values []string, want string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.Contains(v, want) })
}
