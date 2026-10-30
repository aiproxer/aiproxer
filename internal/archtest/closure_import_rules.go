package archtest

// ClosureForbiddenImports extends ForbiddenImports with the ownership-closure
// ratchets (Task 11.2, Req 12.1/12.2). It lives in its own table so the core
// rule file stays under the archtest file-size cap; scanners evaluate both
// slices together through allForbiddenImportRules.
var ClosureForbiddenImports = []ForbiddenImportRule{
	// Public contract and SDK packages stay on public contracts: no internal
	// packages, no SQL/ORM, no provider SDKs, and no optional connector modules.
	{SourcePattern: "pkg/aipapi", TargetPattern: "/internal/", Reason: "canonical contracts must not import internal packages"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "/connectors/", Reason: "canonical contracts must not import optional connector modules"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "/connector-support/", Reason: "canonical contracts must not import optional connector support"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "github.com/openai/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "github.com/anthropics/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "github.com/aws/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "google.golang.org/genai", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "/internal/", Reason: "public plugin SDK must not import internal packages"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "/connectors/", Reason: "public plugin SDK must not import optional connector modules"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "/connector-support/", Reason: "public plugin SDK must not import optional connector support"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "github.com/openai/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "github.com/anthropics/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "github.com/aws/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "google.golang.org/genai", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "github.com/google/generative-ai-go/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "database/sql", Reason: "public plugin SDK contracts must not depend on SQL"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "github.com/uptrace/bun", Reason: "public plugin SDK contracts must not depend on an ORM"},
	{
		SourcePattern: "pkg/aipruntime",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "public facade must not depend on concrete feature plugins; use SDK registrations",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/core",
		Reason:        "feature tree must not depend on internal/core (use pkg/aipsdk contracts)",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "feature tree must not depend on runtimebundle",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/standardplugins/featurehost",
		Reason:        "feature tree must not depend on standard featurehost composition",
	},
	// Open source never imports the Enterprise tree (open-core-billing-split
	// 1.1, Req 1.2/4.3): dependency direction is Enterprise -> open source
	// only, in code, tests, or links.
	{SourcePattern: "cmd", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "internal", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "pkg", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "tools", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "connectors", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "connector-support", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	{SourcePattern: "testdata", TargetPattern: "/enterprise/", Reason: "open source must not import the Enterprise tree"},
	// Bare Enterprise package (open-core-billing-split 1.1, Req 1.2/4.3): the
	// "/enterprise/" rows above match descendants only under the shared
	// matcher's original semantics, so one exact-path row per root covers the
	// bare package itself. Exact patterns match the package only, never its
	// subpackages, per matchImportTarget.
	{SourcePattern: "cmd", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "internal", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "pkg", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "tools", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "connectors", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "connector-support", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
	{SourcePattern: "testdata", TargetPattern: "github.com/aiproxer/aiproxer/enterprise", Reason: "open source must not import the bare Enterprise package"},
}

func allForbiddenImportRules() []ForbiddenImportRule {
	out := make([]ForbiddenImportRule, 0, len(ForbiddenImports)+len(ClosureForbiddenImports))
	out = append(out, ForbiddenImports...)
	out = append(out, ClosureForbiddenImports...)
	return out
}
