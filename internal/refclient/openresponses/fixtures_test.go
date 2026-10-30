package openresponses

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Pinned digests of the immutable official examples (mirrors the profile manifest).
const (
	OfficialParamFixtureDigest    = "sha256:34bdc5059a09c5ead8d1dbe4b8981d7f782c8e205f256e672ae91d67756d3331"
	OfficialResourceFixtureDigest = "sha256:f3787e0361ffdfd1ffd78a3535e91bad6e45468a50e0ac697ad9d2b500153791"
)

// manifestArtifact mirrors the pinned manifest entry.
type manifestArtifact struct {
	Role    string `json:"role"`
	RelPath string `json:"rel_path"`
	SHA256  string `json:"sha256"`
}

type manifestFile struct {
	Artifacts []manifestArtifact `json:"artifacts"`
}

func readManifest(t *testing.T) manifestFile {
	t.Helper()
	b, err := os.ReadFile("testdata/manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifestFile
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(m.Artifacts) == 0 {
		t.Fatal("manifest lists no artifacts")
	}
	return m
}

// TestFixtureManifest_ImmutableDigests proves every pinned fixture byte-for-byte
// matches its committed sha256 digest. Any upstream re-copy requires a manifest update.
func TestFixtureManifest_ImmutableDigests(t *testing.T) {
	t.Parallel()
	m := readManifest(t)
	for _, a := range m.Artifacts {
		t.Run(a.RelPath, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + a.RelPath)
			if err != nil {
				t.Fatalf("read fixture %s: %v", a.RelPath, err)
			}
			sum := sha256.Sum256(b)
			got := "sha256:" + hex.EncodeToString(sum[:])
			if got != a.SHA256 {
				t.Fatalf("digest mismatch for %s: got %s want %s", a.RelPath, got, a.SHA256)
			}
		})
	}
}

// TestOfficialFixtures_ArePinnedToProfileCommit asserts the official examples are the
// pinned upstream artifacts recorded by the spec profile manifest.
func TestOfficialFixtures_ArePinnedToProfileCommit(t *testing.T) {
	t.Parallel()
	param, err := os.ReadFile("testdata/official_examples/ResponseParam.json")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := os.ReadFile("testdata/official_examples/ResponseResource.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(param)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != OfficialParamFixtureDigest {
		t.Fatalf("ResponseParam.json digest %s != %s", got, OfficialParamFixtureDigest)
	}
	sum = sha256.Sum256(resource)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != OfficialResourceFixtureDigest {
		t.Fatalf("ResponseResource.json digest %s != %s", got, OfficialResourceFixtureDigest)
	}
}

// TestOfficialResponseResource_Parses asserts the pinned official response example
// decodes into the independent wire model without semantic loss.
func TestOfficialResponseResource_Parses(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/official_examples/ResponseResource.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseResponseResource(b, DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseResponseResource: %v", err)
	}
	if res.ID != "resp_5a3e04d550c84a63a1d4fc4e3e206abb" {
		t.Fatalf("id: got %q", res.ID)
	}
	if res.Object != "response" || res.Status != "completed" {
		t.Fatalf("object/status: %q/%q", res.Object, res.Status)
	}
	if len(res.Output) != 1 {
		t.Fatalf("output len: got %d", len(res.Output))
	}
	item := res.Output[0]
	if item.Type != ItemMessage || item.Role != "assistant" || item.Status != "completed" {
		t.Fatalf("item: %+v", item)
	}
	if len(item.Content) != 1 {
		t.Fatalf("content len: got %d", len(item.Content))
	}
	if item.Content[0].Text != "Here is an example of a response object in the specified format. Every required property is present and populated with readable, example values. Log probability details are minimized for readability." {
		t.Fatalf("unexpected output text")
	}
	if res.Usage.TotalTokens != 67 {
		t.Fatalf("total tokens: got %d", res.Usage.TotalTokens)
	}
	if res.ParallelToolCalls != false || res.Store != true {
		t.Fatalf("parallel/store: %v/%v", res.ParallelToolCalls, res.Store)
	}
	if res.Temperature == nil || *res.Temperature != 0.7 {
		t.Fatalf("temperature: %v", res.Temperature)
	}
	if res.TopP == nil || *res.TopP != 1 {
		t.Fatalf("top_p: %v", res.TopP)
	}
	if res.Truncation != "disabled" || res.ServiceTier != "standard" {
		t.Fatalf("truncation/service_tier: %q/%q", res.Truncation, res.ServiceTier)
	}
	if res.PreviousResponseID != nil {
		t.Fatalf("previous_response_id should be null, got %q", *res.PreviousResponseID)
	}
	if res.Error != nil {
		t.Fatalf("error should be null, got %+v", res.Error)
	}
}

// TestOfficialResponseParam_Parses asserts the pinned official request example maps
// into the independent request wire model.
func TestOfficialResponseParam_Parses(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/official_examples/ResponseParam.json")
	if err != nil {
		t.Fatal(err)
	}
	var p CreateParams
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal ResponseParam: %v", err)
	}
	if p.Input.TextSet {
		t.Fatalf("official param input must be an item array, got text %q", p.Input.Text)
	}
	if len(p.Input.Items) != 1 {
		t.Fatalf("expected one input item, got %+v", p.Input)
	}
	item := p.Input.Items[0]
	if item.Type != ItemMessage || item.Role != "user" || item.Status != "received" {
		t.Fatalf("input item: %+v", item)
	}
	if len(item.Content) != 1 || item.Content[0].Type != "input_text" || item.Content[0].Text != "What's the weather like in Paris today?" {
		t.Fatalf("content: %+v", item.Content)
	}
	if p.Model != "" {
		t.Fatalf("model should be null (zero), got %q", p.Model)
	}
}

func TestFixtureManifest_NoEmptyScenario(t *testing.T) {
	t.Parallel()
	m := readManifest(t)
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		b, err := os.ReadFile("testdata/" + a.RelPath)
		if err != nil {
			t.Fatalf("read %s: %v", a.RelPath, err)
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			t.Errorf("fixture %s is empty", a.RelPath)
		}
		seen[a.RelPath] = true
	}
	for _, required := range []string{
		"official_examples/ResponseParam.json",
		"official_examples/ResponseResource.json",
		"scenarios/response_text.json",
		"scenarios/response_tools.json",
		"scenarios/response_reasoning.json",
		"scenarios/response_phase.json",
		"scenarios/response_extensions.json",
		"scenarios/response_error.json",
		"scenarios/compact_resource.json",
		"scenarios/request_multimodal.json",
		"scenarios/stream_text.sse",
	} {
		if !seen[required] {
			t.Errorf("required fixture %s missing from manifest", required)
		}
	}
}
