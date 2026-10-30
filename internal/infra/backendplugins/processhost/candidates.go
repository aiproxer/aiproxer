package processhost

type CandidateID string

const (
	CandidateStockGoPluginV180  CandidateID = "stock_go_plugin_v1_8_0"
	CandidateCustomGoPluginV18x CandidateID = "customized_go_plugin_v1_8_x"
	CandidateProjectOwnedHost   CandidateID = "project_owned_host"
)

type EvidenceLevel string

const (
	EvidenceFailed          EvidenceLevel = "failed"
	EvidenceMissing         EvidenceLevel = "missing"
	EvidenceSourceVerified  EvidenceLevel = "source_verified"
	EvidenceRuntimeVerified EvidenceLevel = "runtime_verified"
)

type SourceRef struct {
	Module     string
	Version    string
	Path       string
	Symbol     string
	Lines      string
	URL        string
	License    string
	LicenseURL string
}

type DimensionEvidence struct {
	Requirement   RequirementID
	Level         EvidenceLevel
	Notes         string
	Source        *SourceRef
	ReplacesStock bool
}

type OfficialSourceEvidence struct {
	ModulePath string
	Version    string
	License    string
	SourceURL  string
	LicenseURL string
}

func DefaultOfficialSource() OfficialSourceEvidence {
	return OfficialSourceEvidence{
		ModulePath: "github.com/hashicorp/go-plugin",
		Version:    "v1.8.0",
		License:    "MPL-2.0",
		SourceURL:  "https://github.com/hashicorp/go-plugin/releases/tag/v1.8.0",
		LicenseURL: "https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE",
	}
}

func feasibleLevel(level EvidenceLevel) bool {
	return level == EvidenceSourceVerified || level == EvidenceRuntimeVerified
}
