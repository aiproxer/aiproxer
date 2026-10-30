package providerprofiles_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// expectedProfile defines the stable test-only characterization contract for
// provider profiles embedded in the standard catalog (Tasks 1.3, 2.1-2.4, 3.1-3.3, 4.1-4.4).
type expectedProfile struct {
	ID           string
	Family       providerprofiles.Family
	BaseURL      string
	AuthMode     providerprofiles.AuthMode
	EnvVar       string
	Discovery    providerprofiles.DiscoveryPolicy
	StaticModels []providerprofiles.Model
	DisabledCaps []aipapi.Capability
	Quirks       []providerprofiles.QuirkID
}

// expectedCatalogProfiles lists the incremental expected profiles for landed rows.
// Each batch in Tasks 2-4 extends this table in the same commit.
var expectedCatalogProfiles = []expectedProfile{
	{
		ID: "typesafe", Family: providerprofiles.FamilySystemOne, BaseURL: "https://api.typesafe.ai/v1", AuthMode: providerprofiles.AuthBearerEnv, EnvVar: "TYPESAFE_API_KEY", Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{{CanonicalID: "jev-latest", NativeID: "jev-latest", DisplayName: "jev-latest"}, {CanonicalID: "jev-preview", NativeID: "jev-preview", DisplayName: "jev-preview"}, {CanonicalID: "jev-1.13.0", NativeID: "jev-1.13.0", DisplayName: "jev-1.13.0"}},
	},
	{
		ID: "openrouter-systemone", Family: providerprofiles.FamilySystemOne, BaseURL: "https://openrouter.ai/api/v1", AuthMode: providerprofiles.AuthBearerEnv, EnvVar: "OPENROUTER_API_KEY", Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{{CanonicalID: "typesafe/jev-1.13", NativeID: "typesafe/jev-1.13", DisplayName: "typesafe/jev-1.13"}, {CanonicalID: "~typesafe/jev-latest", NativeID: "~typesafe/jev-latest", DisplayName: "~typesafe/jev-latest"}},
	},
	{
		ID: "commandcode-systemone", Family: providerprofiles.FamilySystemOne, BaseURL: "https://api.commandcode.ai/provider/v1", AuthMode: providerprofiles.AuthBearerEnv, EnvVar: "CMD_API_KEY", Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{{CanonicalID: "typesafe/jev", NativeID: "typesafe/jev", DisplayName: "typesafe/jev"}},
	},
	{
		ID: "subquadratic-systemone", Family: providerprofiles.FamilySystemOne, BaseURL: "https://api.subq.ai/v1", AuthMode: providerprofiles.AuthBearerEnv, EnvVar: "SUBQ_API_KEY", Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{{CanonicalID: "subq-snap-preview", NativeID: "subq-snap-preview", DisplayName: "SubQ Snap Preview"}},
	},
	{
		ID:        "groq",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.groq.com/openai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "GROQ_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "fireworks",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.fireworks.ai/inference/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "FIREWORKS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "digitalocean",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://inference.do-ai.run/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DIGITALOCEAN_ACCESS_TOKEN",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "vercel-ai-gateway",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://ai-gateway.vercel.sh/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AI_GATEWAY_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "requesty",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://router.requesty.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "REQUESTY_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "meta",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.meta.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "META_MODEL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "deepseek-responses",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.deepseek.com",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DEEPSEEK_API_KEY",
		Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{
			{
				CanonicalID: "deepseek-v4-flash",
				NativeID:    "deepseek-v4-flash",
				DisplayName: "DeepSeek V4 Flash",
			},
		},
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
		},
		Quirks: []providerprofiles.QuirkID{
			providerprofiles.QuirkOpenAIResponsesToolAdjacency,
		},
	},
	{
		ID:        "deepseek-openai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.deepseek.com",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DEEPSEEK_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
		},
		Quirks: []providerprofiles.QuirkID{
			providerprofiles.QuirkOpenAIChatReasoningContent,
		},
	},
	{
		ID:        "scaleway-responses",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.scaleway.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SCW_SECRET_KEY",
		Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{
			{
				CanonicalID: "openai/gpt-oss-120b:fp4",
				NativeID:    "openai/gpt-oss-120b:fp4",
				DisplayName: "GPT-OSS 120B FP4",
			},
			{
				CanonicalID: "openai/gpt-oss-20b:fp4",
				NativeID:    "openai/gpt-oss-20b:fp4",
				DisplayName: "GPT-OSS 20B FP4",
			},
		},
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "scaleway-openai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.scaleway.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SCW_SECRET_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "302ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.302.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "302AI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "abacus",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://routellm.abacus.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ABACUS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "abliteration-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.abliteration.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ABLIT_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "ai-router",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.ai-router.dev/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AI_ROUTER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "aiand",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.aiand.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AIAND_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "aihubmix",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.aihubmix.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AIHUBMIX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "aki-io",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://aki.io/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AKI_IO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "alibaba",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DASHSCOPE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "alibaba-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DASHSCOPE_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "alibaba-coding-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://coding-intl.dashscope.aliyuncs.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ALIBABA_CODING_PLAN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "alibaba-coding-plan-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://coding.dashscope.aliyuncs.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ALIBABA_CODING_PLAN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "alibaba-token-plan-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ALIBABA_TOKEN_PLAN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "ambient",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.ambient.xyz/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AMBIENT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "amd",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://developer.amd.com.cn/radeon/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AMD_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "anyapi",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.anyapi.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ANYAPI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "arcee",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.arcee.ai/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ARCEE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "auriko",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.auriko.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "AURIKO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "baseten",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.baseten.co/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "BASETEN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "berget",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.berget.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "BERGET_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "blueclaw",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://openai.blueclaw.network/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "BLUECLAW_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "cerebras",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.cerebras.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CEREBRAS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "chutes",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://llm.chutes.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CHUTES_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "clarifai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.clarifai.com/v2/ext/openai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CLARIFAI_PAT",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "claudinio",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.claudin.io/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CLAUDINIO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "cline-pass",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.cline.bot/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CLINE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "cloudferro-sherlock",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api-sherlock.cloudferro.com/openai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CLOUDFERRO_SHERLOCK_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "coralbricks",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.coralbricks.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CORAL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "cortecs",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.cortecs.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CORTECS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "crof",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://crof.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CROF_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "crossmodel",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.crossmodel.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CROSSMODEL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "crusoe",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.inference.crusoecloud.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "CRUSOE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "daoxe",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://daoxe.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DAOXE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "deepinfra",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.deepinfra.com/v1/openai",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DEEPINFRA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
		Quirks: []providerprofiles.QuirkID{
			providerprofiles.QuirkOpenAIPromptCacheKey,
		},
	},
	{
		ID:        "dinference",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.dinference.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DINFERENCE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "drun",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://chat.d.run/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "DRUN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "ebcloud",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://maas-api.ebcloud.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "EBCLOUD_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "echo",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://echo.tracerml.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ECHO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "edenai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.edenai.run/v3",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "EDENAI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "empiriolabs",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.empiriolabs.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "EMPIRIOLABS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "evroc",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://models.think.evroc.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "EVROC_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "fastrouter",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://go.fastrouter.ai/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "FASTROUTER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "friendli",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.friendli.ai/serverless/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "FRIENDLI_TOKEN",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "frogbot",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://app.frogbot.ai/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "FROGBOT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "gmicloud",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.gmi-serving.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "GMICLOUD_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "greenpt",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.greenpt.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "GREENPT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "helicone",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://ai-gateway.helicone.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "HELICONE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "hetzner",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.hetzner.com/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "HETZNER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "hpc-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.hpc-ai.com/inference/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "HPC_AI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "hyper",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://hyper.charm.land/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "HYPER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "iflowcn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://apis.iflow.cn/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "IFLOW_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "impossibl",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.impossibl.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "IMPOSSIBL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "inception",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.inceptionlabs.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "INCEPTION_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "inceptron",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.inceptron.io/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "INCEPTRON_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "inference-net",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.net/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "INFERENCE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "inferx",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://model.inferx.net/endpoints/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "INFERX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "io-net",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.intelligence.io.solutions/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "IOINTELLIGENCE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "jalapeno",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.jalapeno-cloud.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "JALAPENO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "jiekou",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.jiekou.ai/openai",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "JIEKOU_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "kenari",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://kenari.id/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "KENARI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "kilo",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.kilo.ai/api/gateway",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "KILO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "llmgateway",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.llmgateway.io/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "LLMGATEWAY_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "llmtech",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.llmtech.eu/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "LLMTECH_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "llmtr",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://llmtr.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "LLMTR_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "longcat",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.longcat.chat/openai",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "LONGCAT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "lucidquery",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.lucidquery.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "LUCIDQUERY_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "meganova",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.meganova.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MEGANOVA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "mistral",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.mistral.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MISTRAL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
		Quirks: []providerprofiles.QuirkID{
			providerprofiles.QuirkOpenAIPromptCacheKey,
		},
	},
	{
		ID:        "mixlayer",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://models.mixlayer.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MIXLAYER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "moark",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://moark.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MOARK_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "modal",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.us-west.modal.direct/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MODAL_PROXY_TOKEN",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "model-oracle-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.modeloracle.com/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MODEL_ORACLE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "modelis",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://modelishub.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MODELIS_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "modelscope",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api-inference.modelscope.cn/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MODELSCOPE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "moonshot",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.moonshot.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MOONSHOT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "moonshot-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.moonshot.cn/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MOONSHOT_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "morph",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.morphllm.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "MORPH_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "neuralwatt",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.neuralwatt.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "NEURALWATT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "nova",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.nova.amazon.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "NOVA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "novita-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.novita.ai/openai",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "NOVITA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "ofox",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.ofox.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "OFOX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "opper",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.opper.ai/v3/compat",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "OPPER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "orcarouter",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.orcarouter.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ORCAROUTER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "ovhcloud",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://oai.endpoints.kepler.ai.cloud.ovh.net/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "OVHCLOUD_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "pendra",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.pendra.ai/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "PENDRA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "pioneer",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.pioneer.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "PIONEER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "poe",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.poe.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "POE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "poolside",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.poolside.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "POOLSIDE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "qihang-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.qhaigc.net/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "QIHANG_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "qiniu-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.qnaigc.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "QINIU_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "regolo-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.regolo.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "REGOLO_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "routing-run",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.routing.run/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ROUTING_RUN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "scnet-token-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.scnet.cn/api/llm/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SCNET_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "scx-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.scx.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SCX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "siliconflow",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.siliconflow.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SILICONFLOW_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "siliconflow-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.siliconflow.cn/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SILICONFLOW_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "stackit",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.openai-compat.model-serving.eu01.onstackit.cloud/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STACKIT_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "standardcompute",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.stdcmpt.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STANDARDCOMPUTE_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "stepfun",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.stepfun.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STEPFUN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "stepfun-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.stepfun.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STEPFUN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "stepfun-step-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.stepfun.ai/step_plan/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STEPFUN_STEP_PLAN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "stepfun-step-plan-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.stepfun.com/step_plan/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "STEPFUN_STEP_PLAN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "submodel",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://llm.submodel.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SUBMODEL_INSTAGEN_ACCESS_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "synthetic",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.synthetic.new/openai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "SYNTHETIC_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "tencent-coding-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.lkeap.cloud.tencent.com/coding/v3",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TENCENT_CODING_PLAN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "tencent-token-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.lkeap.cloud.tencent.com/plan/v3",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TENCENT_TOKEN_PLAN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "tencent-tokenhub",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://tokenhub.tencentmaas.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TENCENT_TOKENHUB_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "tensorx",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.tensorx.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TENSORX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "the-grid-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.thegrid.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "THEGRID_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "tinfoil",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://inference.tinfoil.sh/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TINFOIL_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "together",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.together.xyz/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TOGETHER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "trustedrouter",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.trustedrouter.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "TRUSTEDROUTER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "vultr",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.vultrinference.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "VULTR_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "wafer-ai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://pass.wafer.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "WAFER_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "wandb",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.inference.wandb.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "WANDB_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.x.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XAI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xiaomi",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.xiaomimimo.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XIAOMI_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xiaomi-token-plan-eu",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://token-plan-ams.xiaomimimo.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XIAOMI_TOKEN_PLAN_EU_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xiaomi-token-plan-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://token-plan-cn.xiaomimimo.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XIAOMI_TOKEN_PLAN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xiaomi-token-plan-sg",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://token-plan-sgp.xiaomimimo.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XIAOMI_TOKEN_PLAN_SG_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "xpersona",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://www.xpersona.co/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "XPERSONA_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zai",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.z.ai/api/paas/v4",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZHIPU_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zai-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://open.bigmodel.cn/api/paas/v4",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZHIPU_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zai-coding-plan",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.z.ai/api/coding/paas/v4",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZHIPU_CODING_PLAN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zai-coding-plan-cn",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://open.bigmodel.cn/api/coding/paas/v4",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZHIPU_CODING_PLAN_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zeldoc",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://api.zeldoc.ai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZELDOC_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zenifra",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://ai.zenifra.com/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZENIFRA_AI_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "zenmux",
		Family:    providerprofiles.FamilyOpenAIChat,
		BaseURL:   "https://zenmux.ai/api/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "ZENMUX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	},
	{
		ID:        "kimi-coding",
		Family:    providerprofiles.FamilyAnthropic,
		BaseURL:   "https://api.kimi.com/coding/",
		AuthMode:  providerprofiles.AuthAPIKeyEnv,
		EnvVar:    "KIMI_API_KEY",
		Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{
			{
				CanonicalID: "k3",
				NativeID:    "k3",
				DisplayName: "Kimi K3",
			},
			{
				CanonicalID: "k3-256k",
				NativeID:    "k3-256k",
				DisplayName: "Kimi K3 256K",
			},
			{
				CanonicalID: "kimi-for-coding",
				NativeID:    "kimi-for-coding",
				DisplayName: "Kimi for Coding",
			},
			{
				CanonicalID: "kimi-for-coding-highspeed",
				NativeID:    "kimi-for-coding-highspeed",
				DisplayName: "Kimi for Coding High-Speed",
			},
		},
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
			aipapi.CapabilityReasoningReplay,
		},
	},
	{
		ID:        "minimax",
		Family:    providerprofiles.FamilyAnthropic,
		BaseURL:   "https://api.minimax.io/anthropic",
		AuthMode:  providerprofiles.AuthAPIKeyEnv,
		EnvVar:    "MINIMAX_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
			aipapi.CapabilityReasoningReplay,
		},
	},
	{
		ID:        "minimax-cn",
		Family:    providerprofiles.FamilyAnthropic,
		BaseURL:   "https://api.minimaxi.com/anthropic",
		AuthMode:  providerprofiles.AuthAPIKeyEnv,
		EnvVar:    "MINIMAX_CN_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
			aipapi.CapabilityReasoningReplay,
		},
	},
	{
		ID:        "thinking-machines",
		Family:    providerprofiles.FamilyAnthropic,
		BaseURL:   "https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api",
		AuthMode:  providerprofiles.AuthAPIKeyEnv,
		EnvVar:    "TINKER_API_KEY",
		Discovery: providerprofiles.DiscoveryStatic,
		StaticModels: []providerprofiles.Model{
			{
				CanonicalID: "thinkingmachines/Inkling",
				NativeID:    "thinkingmachines/Inkling",
				DisplayName: "Thinking Machines Inkling",
			},
		},
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityParallelToolCalls,
			aipapi.CapabilityReasoningReplay,
		},
	},
}

// dedicatedBackendAndConnectorIDs are existing built-in or connector backend IDs
// that provider profiles must never collide with.
var dedicatedBackendAndConnectorIDs = map[string]bool{
	"openai-responses":        true,
	"openai-chat":             true,
	"openai-legacy":           true,
	"anthropic":               true,
	"gemini":                  true,
	"bedrock":                 true,
	"alibaba-token-plan-intl": true,
	"acp":                     true,
	"agycliacp":               true,
	"codex":                   true,
	"commandcode-anthropic":   true,
	"commandcode-openai":      true,
	"cursorcliacp":            true,
	"geminicliacp":            true,
	"huggingface":             true,
	"llamacpp":                true,
	"lmstudio":                true,
	"localstub":               true,
	"nvidia":                  true,
	"ollama":                  true,
	"opencode":                true,
	"opencode-go":             true,
	"opencode-zen":            true,
	"openai-codex":            true,
	"openrouter":              true,
	"vllm":                    true,
	// Bulk provider expansion spec connectors
	"cloudflare":        true,
	"azure-openai":      true,
	"snowflake-cortex":  true,
	"databricks-ai":     true,
	"infomaniak-ai":     true,
	"vertex":            true,
	"sagemaker":         true,
	"oci-generative-ai": true,
	"watsonx":           true,
	"sapaicore":         true,
	"cohere":            true,
	"replicate":         true,
	"gitlab-duo":        true,
	"nous-portal":       true,
	"xai-oauth":         true,
	"qwen-oauth":        true,
	"minimax-oauth":     true,
}

func isDedicatedProductOrFamily(id string) bool {
	// The approved decision family is separate from Command Code's existing
	// executable chat connectors; its profile does not replace their IDs.
	if id == "commandcode-systemone" {
		return false
	}
	if dedicatedBackendAndConnectorIDs[id] {
		return true
	}
	if strings.HasPrefix(id, "commandcode-") ||
		strings.HasPrefix(id, "ollama") ||
		strings.HasPrefix(id, "opencode-") {
		return true
	}
	return false
}

func validateProfileMatch(expected expectedProfile, actual providerprofiles.Profile) error {
	if actual.ID != expected.ID {
		return fmt.Errorf("ID mismatch: got %q, want %q", actual.ID, expected.ID)
	}
	if actual.Family != expected.Family {
		return fmt.Errorf("%s: Family mismatch: got %q, want %q", actual.ID, actual.Family, expected.Family)
	}
	if actual.Endpoint.BaseURL != expected.BaseURL {
		return fmt.Errorf("%s: BaseURL mismatch: got %q, want %q", actual.ID, actual.Endpoint.BaseURL, expected.BaseURL)
	}
	if actual.Auth.Mode != expected.AuthMode {
		return fmt.Errorf("%s: AuthMode mismatch: got %q, want %q", actual.ID, actual.Auth.Mode, expected.AuthMode)
	}
	if actual.Auth.EnvVar != expected.EnvVar {
		return fmt.Errorf("%s: EnvVar mismatch: got %q, want %q", actual.ID, actual.Auth.EnvVar, expected.EnvVar)
	}
	if actual.Models.Policy != expected.Discovery {
		return fmt.Errorf("%s: Discovery policy mismatch: got %q, want %q", actual.ID, actual.Models.Policy, expected.Discovery)
	}

	// Compare disabled capabilities
	actualDisabled := append([]aipapi.Capability(nil), actual.Capabilities.Disable...)
	expectedDisabled := append([]aipapi.Capability(nil), expected.DisabledCaps...)
	slices.Sort(actualDisabled)
	slices.Sort(expectedDisabled)
	if !slices.Equal(actualDisabled, expectedDisabled) {
		return fmt.Errorf("%s: DisabledCaps mismatch: got %v, want %v", actual.ID, actualDisabled, expectedDisabled)
	}

	// Compare static models
	if len(actual.Models.Static) != len(expected.StaticModels) {
		return fmt.Errorf("%s: StaticModels count mismatch: got %d, want %d", actual.ID, len(actual.Models.Static), len(expected.StaticModels))
	}
	for i, sm := range expected.StaticModels {
		act := actual.Models.Static[i]
		if act.CanonicalID != sm.CanonicalID || act.NativeID != sm.NativeID || act.DisplayName != sm.DisplayName {
			return fmt.Errorf("%s: StaticModel[%d] mismatch: got %+v, want %+v", actual.ID, i, act, sm)
		}
	}

	// Compare quirks (order-insensitive).
	actualQuirks := append([]providerprofiles.QuirkID(nil), actual.Quirks...)
	expectedQuirks := append([]providerprofiles.QuirkID(nil), expected.Quirks...)
	slices.Sort(actualQuirks)
	slices.Sort(expectedQuirks)
	if !slices.Equal(actualQuirks, expectedQuirks) {
		return fmt.Errorf("%s: Quirks mismatch: got %v, want %v", actual.ID, actualQuirks, expectedQuirks)
	}
	return nil
}

func TestCatalogPopulation_Characterization(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}

	profiles := cat.Profiles()
	actualByID := make(map[string]providerprofiles.Profile, len(profiles))
	for _, p := range profiles {
		actualByID[p.ID] = p
	}

	expectedByID := make(map[string]expectedProfile, len(expectedCatalogProfiles))
	for _, exp := range expectedCatalogProfiles {
		expectedByID[exp.ID] = exp
	}

	// 1. Assert every expected profile exists and matches stable fields
	for _, exp := range expectedCatalogProfiles {
		actual, ok := actualByID[exp.ID]
		if !ok {
			t.Fatalf("expected profile %q missing from embedded catalog", exp.ID)
		}
		if err := validateProfileMatch(exp, actual); err != nil {
			t.Fatalf("profile validation failed: %v", err)
		}
	}

	// 2. Assert no undeclared profiles exist in catalog
	for _, actual := range profiles {
		if _, ok := expectedByID[actual.ID]; !ok {
			t.Fatalf("embedded catalog contains unexpected profile %q not in expected characterization table", actual.ID)
		}
	}

	// 3. Assert guardrails across all embedded profiles
	for _, p := range profiles {
		// No ACP IDs
		if strings.Contains(strings.ToLower(p.ID), "acp") {
			t.Fatalf("profile %q contains forbidden 'acp' in ID", p.ID)
		}

		// No collisions with dedicated backends or connectors
		if isDedicatedProductOrFamily(p.ID) {
			t.Fatalf("profile %q collides with dedicated backend, connector, or product family", p.ID)
		}

		// Valid endpoint scheme
		if !strings.HasPrefix(p.Endpoint.BaseURL, "https://") && !strings.HasPrefix(p.Endpoint.BaseURL, "http://") {
			t.Fatalf("profile %q has invalid endpoint BaseURL: %q", p.ID, p.Endpoint.BaseURL)
		}

		// Valid auth mode and env var
		if p.Auth.Mode != providerprofiles.AuthBearerEnv && p.Auth.Mode != providerprofiles.AuthAPIKeyEnv && p.Auth.Mode != providerprofiles.AuthNone {
			t.Fatalf("profile %q has unsupported auth mode: %q", p.ID, p.Auth.Mode)
		}
		if p.Auth.Mode != providerprofiles.AuthNone {
			if p.Auth.EnvVar == "" {
				t.Fatalf("profile %q has empty EnvVar for auth mode %q", p.ID, p.Auth.Mode)
			}
			if p.Auth.EnvVar != strings.ToUpper(p.Auth.EnvVar) {
				t.Fatalf("profile %q EnvVar %q must be uppercase", p.ID, p.Auth.EnvVar)
			}
		}

		// Every static model must have complete identity
		for i, sm := range p.Models.Static {
			if sm.CanonicalID == "" {
				t.Fatalf("profile %q static model [%d] has empty CanonicalID", p.ID, i)
			}
			if sm.NativeID == "" {
				t.Fatalf("profile %q static model [%d] has empty NativeID", p.ID, i)
			}
			if sm.DisplayName == "" {
				t.Fatalf("profile %q static model [%d] has empty DisplayName", p.ID, i)
			}
		}
	}

	// 4. Assert required suffix pairs exist together: fail if one side is missing
	for _, pair := range requiredSuffixPairs {
		_, hasA := actualByID[pair[0]]
		_, hasB := actualByID[pair[1]]
		if !hasA || !hasB {
			t.Fatalf("required suffix pair missing: %q (found=%v) and %q (found=%v)", pair[0], hasA, pair[1], hasB)
		}
	}
}

// requiredSuffixPairs defines pairs of flavor split profiles that must exist together.
var requiredSuffixPairs = [][2]string{
	{"deepseek-responses", "deepseek-openai"},
	{"scaleway-responses", "scaleway-openai"},
}

func checkRequiredSuffixPairs(profiles []providerprofiles.Profile) error {
	byID := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = true
	}
	for _, pair := range requiredSuffixPairs {
		hasA := byID[pair[0]]
		hasB := byID[pair[1]]
		if hasA != hasB {
			return fmt.Errorf("required suffix pair broken: %q (found=%v) and %q (found=%v) must exist together", pair[0], hasA, pair[1], hasB)
		}
	}
	return nil
}

func TestCatalogPopulation_RequiredSuffixPairs(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	profiles := cat.Profiles()
	if err := checkRequiredSuffixPairs(profiles); err != nil {
		t.Fatalf("checkRequiredSuffixPairs: %v", err)
	}

	byID := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = true
	}
	for _, pair := range requiredSuffixPairs {
		if !byID[pair[0]] || !byID[pair[1]] {
			t.Fatalf("expected both pair members in embedded catalog: %q=%v, %q=%v", pair[0], byID[pair[0]], pair[1], byID[pair[1]])
		}
	}
}

func TestCatalogPopulation_RequiredSuffixPairs_DetectsMissingPartner(t *testing.T) {
	t.Parallel()
	for _, pair := range requiredSuffixPairs {
		// Only side A present
		profilesA := []providerprofiles.Profile{{ID: pair[0]}}
		if err := checkRequiredSuffixPairs(profilesA); err == nil {
			t.Fatalf("expected error when %q is present but %q is missing", pair[0], pair[1])
		}
		// Only side B present
		profilesB := []providerprofiles.Profile{{ID: pair[1]}}
		if err := checkRequiredSuffixPairs(profilesB); err == nil {
			t.Fatalf("expected error when %q is present but %q is missing", pair[1], pair[0])
		}
	}
}

// independentProductGroups defines the explicit rule table of independent region and plan
// products named in Task 1.3, Requirement 4.7, and research.md:
// Alibaba, Moonshot, StepFun, Xiaomi, Z.AI, and MiniMax.
// When two of these independent product IDs exist in the catalog, their Auth.EnvVar must differ.
// Deliberate same-product protocol splits (e.g. future deepseek-responses + deepseek-openai) may share.
var independentProductGroups = map[string][]string{
	"alibaba": {
		"alibaba",
		"alibaba-cn",
		"alibaba-coding-plan",
		"alibaba-coding-plan-cn",
		"alibaba-token-plan-cn",
	},
	"moonshot": {
		"moonshot",
		"moonshot-cn",
	},
	"stepfun": {
		"stepfun",
		"stepfun-cn",
		"stepfun-step-plan",
		"stepfun-step-plan-cn",
	},
	"xiaomi": {
		"xiaomi",
		"xiaomi-token-plan-eu",
		"xiaomi-token-plan-cn",
		"xiaomi-token-plan-sg",
	},
	"zai": {
		"zai",
		"zai-cn",
		"zai-coding-plan",
		"zai-coding-plan-cn",
	},
	"minimax": {
		"minimax",
		"minimax-cn",
	},
}

func checkCredentialIsolation(profiles []providerprofiles.Profile) error {
	byID := make(map[string]providerprofiles.Profile, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = p
	}

	for group, productIDs := range independentProductGroups {
		envVarToProfile := make(map[string]string)
		for _, id := range productIDs {
			p, ok := byID[id]
			if !ok || p.Auth.EnvVar == "" {
				continue
			}
			if existingID, exists := envVarToProfile[p.Auth.EnvVar]; exists && existingID != id {
				return fmt.Errorf("vendor group %q: independent products %q and %q share the same credential root %q",
					group, existingID, id, p.Auth.EnvVar)
			}
			envVarToProfile[p.Auth.EnvVar] = id
		}
	}
	return nil
}

func TestCatalogPopulation_BrandCredentialIsolation(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}

	if err := checkCredentialIsolation(cat.Profiles()); err != nil {
		t.Fatalf("brand credential isolation failed: %v", err)
	}
}

func TestCatalogPopulation_BrandCredentialIsolation_DetectsSharedRoot(t *testing.T) {
	t.Parallel()
	// Prove that when two independent region/plan products in the same vendor group
	// share an env var, checkCredentialIsolation fails.
	for group, productIDs := range independentProductGroups {
		if len(productIDs) < 2 {
			continue
		}
		collidingProfiles := []providerprofiles.Profile{
			{
				ID:   productIDs[0],
				Auth: providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "SHARED_ROOT_KEY"},
			},
			{
				ID:   productIDs[1],
				Auth: providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "SHARED_ROOT_KEY"},
			},
		}
		if err := checkCredentialIsolation(collidingProfiles); err == nil {
			t.Fatalf("expected collision error for vendor group %q when %q and %q share env var",
				group, productIDs[0], productIDs[1])
		}
	}
}

func TestCatalogPopulation_Alibaba_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate alibaba-cn to share DASHSCOPE_API_KEY with alibaba
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "alibaba-cn" {
			profiles[i].Auth.EnvVar = "DASHSCOPE_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when alibaba and alibaba-cn share DASHSCOPE_API_KEY")
	}
}

func TestCatalogPopulation_Moonshot_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate moonshot-cn to share MOONSHOT_API_KEY with moonshot
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "moonshot-cn" {
			profiles[i].Auth.EnvVar = "MOONSHOT_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when moonshot and moonshot-cn share MOONSHOT_API_KEY")
	}
}

func TestCatalogPopulation_StepFun_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate stepfun-cn to share STEPFUN_API_KEY with stepfun
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "stepfun-cn" {
			profiles[i].Auth.EnvVar = "STEPFUN_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when stepfun and stepfun-cn share STEPFUN_API_KEY")
	}
}

func TestCatalogPopulation_Xiaomi_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate xiaomi-token-plan-cn to share XIAOMI_API_KEY with xiaomi
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "xiaomi-token-plan-cn" {
			profiles[i].Auth.EnvVar = "XIAOMI_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when xiaomi and xiaomi-token-plan-cn share XIAOMI_API_KEY")
	}
}

func TestCatalogPopulation_ZAI_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate zai-cn to share ZHIPU_API_KEY with zai
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "zai-cn" {
			profiles[i].Auth.EnvVar = "ZHIPU_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when zai and zai-cn share ZHIPU_API_KEY")
	}
}

func TestCatalogPopulation_FailClosedOnMutation(t *testing.T) {
	t.Parallel()
	groq, err := providerprofiles.EmbeddedProfile("groq")
	if err != nil {
		t.Fatalf("EmbeddedProfile(groq): %v", err)
	}

	baseExpected := expectedProfile{
		ID:        "groq",
		Family:    providerprofiles.FamilyOpenAIResponses,
		BaseURL:   "https://api.groq.com/openai/v1",
		AuthMode:  providerprofiles.AuthBearerEnv,
		EnvVar:    "GROQ_API_KEY",
		Discovery: providerprofiles.DiscoveryFamilyDefault,
		DisabledCaps: []aipapi.Capability{
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
		},
	}

	// Verify base matches
	if err := validateProfileMatch(baseExpected, groq); err != nil {
		t.Fatalf("base match failed: %v", err)
	}

	// Mutate BaseURL -> must fail
	mutated := baseExpected
	mutated.BaseURL = "https://api.mutated.invalid/v1"
	if err := validateProfileMatch(mutated, groq); err == nil {
		t.Fatalf("expected error on mutated BaseURL, got nil")
	}

	// Mutate Family -> must fail
	mutated = baseExpected
	mutated.Family = providerprofiles.FamilyAnthropic
	if err := validateProfileMatch(mutated, groq); err == nil {
		t.Fatalf("expected error on mutated Family, got nil")
	}

	// Mutate EnvVar -> must fail
	mutated = baseExpected
	mutated.EnvVar = "WRONG_KEY"
	if err := validateProfileMatch(mutated, groq); err == nil {
		t.Fatalf("expected error on mutated EnvVar, got nil")
	}

	// Mutate DisabledCaps -> must fail
	mutated = baseExpected
	mutated.DisabledCaps = []aipapi.Capability{aipapi.CapabilityVision}
	if err := validateProfileMatch(mutated, groq); err == nil {
		t.Fatalf("expected error on mutated DisabledCaps, got nil")
	}
}

func TestMeta_OfflineResponsesAndModelDiscoveryFixture(t *testing.T) {
	const testAPIKey = "meta-test-secret-key"
	t.Setenv("META_MODEL_API_KEY", testAPIKey)

	var (
		receivedModelsAuth    atomic.Pointer[string]
		receivedResponsesAuth atomic.Pointer[string]
		receivedResponsesBody atomic.Pointer[string]
		modelsHit             atomic.Int32
		responsesHit          atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models") && r.Method == http.MethodGet:
			modelsHit.Add(1)
			receivedModelsAuth.Store(&auth)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"meta-llama/Llama-3.3-70B-Instruct"}]}`)
		case strings.HasSuffix(r.URL.Path, "/responses") && r.Method == http.MethodPost:
			responsesHit.Add(1)
			receivedResponsesAuth.Store(&auth)
			body, _ := io.ReadAll(r.Body)
			s := string(body)
			receivedResponsesBody.Store(&s)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp-meta-ns","object":"response","created_at":1715620000,"status":"completed","model":"meta-llama/Llama-3.3-70B-Instruct","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from meta"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// 1. Copy the embedded meta profile but set Endpoint.BaseURL to the httptest server family root.
	// Meta's catalog endpoint is https://api.meta.ai/v1, so the family root includes /v1.
	embeddedMeta, err := providerprofiles.EmbeddedProfile("meta")
	if err != nil {
		t.Fatalf("EmbeddedProfile(meta): %v", err)
	}
	testMeta := embeddedMeta
	testMeta.Endpoint.BaseURL = srv.URL + "/v1"

	testCatalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testMeta})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	// 2. Drive PrepareProviderProfilesWithCatalog + InstallStandardBundleOn + BuildBackendWithLifecycle.
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: meta\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "meta-instance", Kind: standardplugins.ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}
	prepared, err := standardplugins.PrepareProviderProfilesWithCatalog(cfg, testCatalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		srv.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// 3. LoadModels must hit GET /models and accept the official {object,data:[{id}]} list shape.
	if backend.ModelInventory == nil {
		t.Fatal("expected non-nil ModelInventory on meta backend")
	}
	snapshot, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if modelsHit.Load() == 0 {
		t.Fatal("expected GET /models to be called on upstream server")
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].NativeID != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Fatalf("unexpected snapshot models: %+v", snapshot.Models)
	}

	// 4. A non-streaming Responses call must hit POST .../responses and decode a completed Responses body with output message/text.
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIResponses,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "meta-instance", Model: "meta-llama/Llama-3.3-70B-Instruct"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if responsesHit.Load() == 0 {
		t.Fatal("expected POST /responses to be called on upstream server")
	}
	if collected.Text.String() != "hello from meta" {
		t.Fatalf("decoded response text mismatch: got %q, want %q", collected.Text.String(), "hello from meta")
	}

	// 5. Assert bearer META_MODEL_API_KEY is sent.
	wantAuth := "Bearer " + testAPIKey
	if auth := receivedModelsAuth.Load(); auth == nil || *auth != wantAuth {
		got := "<nil>"
		if auth != nil {
			got = *auth
		}
		t.Fatalf("GET /models Authorization mismatch: got %q, want %q", got, wantAuth)
	}
	if auth := receivedResponsesAuth.Load(); auth == nil || *auth != wantAuth {
		got := "<nil>"
		if auth != nil {
			got = *auth
		}
		t.Fatalf("POST /responses Authorization mismatch: got %q, want %q", got, wantAuth)
	}
}

func TestDeepSeek_FlavorSplitIsolation(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}

	var respProfile, chatProfile providerprofiles.Profile
	var foundResp, foundChat bool
	for _, p := range cat.Profiles() {
		if p.ID == "deepseek-responses" {
			respProfile = p
			foundResp = true
		}
		if p.ID == "deepseek-openai" {
			chatProfile = p
			foundChat = true
		}
	}
	if !foundResp {
		t.Fatal("deepseek-responses missing from catalog")
	}
	if !foundChat {
		t.Fatal("deepseek-openai missing from catalog")
	}

	// 1. Responses flavor MUST have static inventory
	if respProfile.Models.Policy != providerprofiles.DiscoveryStatic {
		t.Fatalf("deepseek-responses policy=%q, want static", respProfile.Models.Policy)
	}

	// 2. Static inventory contains ONLY deepseek-v4-flash with complete frozen identity
	if len(respProfile.Models.Static) != 1 {
		t.Fatalf("deepseek-responses static models count=%d, want 1", len(respProfile.Models.Static))
	}
	flash := respProfile.Models.Static[0]
	if flash.CanonicalID != "deepseek-v4-flash" || flash.NativeID != "deepseek-v4-flash" || flash.DisplayName != "DeepSeek V4 Flash" {
		t.Fatalf("deepseek-v4-flash identity mismatch: got %+v", flash)
	}

	// 3. Responses inventory can NEVER surface a Pro-only model
	for _, m := range respProfile.Models.Static {
		if strings.Contains(strings.ToLower(m.CanonicalID), "pro") || strings.Contains(strings.ToLower(m.NativeID), "pro") {
			t.Fatalf("deepseek-responses surfaced Pro-only model: %+v", m)
		}
	}

	// 4. Chat flavor uses family_default discovery for broader coverage
	if chatProfile.Models.Policy != providerprofiles.DiscoveryFamilyDefault {
		t.Fatalf("deepseek-openai policy=%q, want family_default", chatProfile.Models.Policy)
	}

	// 5. Reasoning capability is retained on both flavors (not in disabled caps)
	if slices.Contains(respProfile.Capabilities.Disable, aipapi.CapabilityReasoning) {
		t.Fatal("expected reasoning capability to be retained on deepseek-responses")
	}
	if slices.Contains(chatProfile.Capabilities.Disable, aipapi.CapabilityReasoning) {
		t.Fatal("expected reasoning capability to be retained on deepseek-openai")
	}
}

func TestScaleway_FlavorSplitIsolation(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}

	var respProfile, chatProfile providerprofiles.Profile
	var foundResp, foundChat bool
	for _, p := range cat.Profiles() {
		if p.ID == "scaleway-responses" {
			respProfile = p
			foundResp = true
		}
		if p.ID == "scaleway-openai" {
			chatProfile = p
			foundChat = true
		}
	}
	if !foundResp {
		t.Fatal("scaleway-responses missing from catalog")
	}
	if !foundChat {
		t.Fatal("scaleway-openai missing from catalog")
	}

	// 1. Responses flavor MUST have static inventory
	if respProfile.Models.Policy != providerprofiles.DiscoveryStatic {
		t.Fatalf("scaleway-responses policy=%q, want static", respProfile.Models.Policy)
	}

	// 2. Assert complete frozen canonical/native/display identities for every static Responses model
	expectedStatic := []providerprofiles.Model{
		{
			CanonicalID: "openai/gpt-oss-120b:fp4",
			NativeID:    "openai/gpt-oss-120b:fp4",
			DisplayName: "GPT-OSS 120B FP4",
		},
		{
			CanonicalID: "openai/gpt-oss-20b:fp4",
			NativeID:    "openai/gpt-oss-20b:fp4",
			DisplayName: "GPT-OSS 20B FP4",
		},
	}
	if len(respProfile.Models.Static) != len(expectedStatic) {
		t.Fatalf("scaleway-responses static models count=%d, want %d", len(respProfile.Models.Static), len(expectedStatic))
	}
	for i, exp := range expectedStatic {
		got := respProfile.Models.Static[i]
		if got.CanonicalID != exp.CanonicalID || got.NativeID != exp.NativeID || got.DisplayName != exp.DisplayName {
			t.Fatalf("scaleway static model[%d] mismatch: got %+v, want %+v", i, got, exp)
		}
	}

	// 3. Chat-only models cannot appear under Responses
	for _, m := range respProfile.Models.Static {
		if strings.Contains(strings.ToLower(m.CanonicalID), "llama") || strings.Contains(strings.ToLower(m.CanonicalID), "mistral") {
			t.Fatalf("Chat-only model appeared under scaleway-responses: %+v", m)
		}
	}

	// 4. Chat flavor uses family_default discovery for broader Chat set
	if chatProfile.Models.Policy != providerprofiles.DiscoveryFamilyDefault {
		t.Fatalf("scaleway-openai policy=%q, want family_default", chatProfile.Models.Policy)
	}
}

func TestCatalogPopulation_MiniMax_CredentialIsolation_FailsWhenShared(t *testing.T) {
	t.Parallel()
	cat, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("EmbeddedCatalog: %v", err)
	}
	// Copy profiles and mutate minimax-cn to share MINIMAX_API_KEY with minimax
	profiles := make([]providerprofiles.Profile, len(cat.Profiles()))
	copy(profiles, cat.Profiles())
	for i := range profiles {
		if profiles[i].ID == "minimax-cn" {
			profiles[i].Auth.EnvVar = "MINIMAX_API_KEY"
		}
	}
	if err := checkCredentialIsolation(profiles); err == nil {
		t.Fatal("expected error when minimax and minimax-cn share MINIMAX_API_KEY")
	}
}

func TestCatalogPopulation_DedicatedProductCollisions_Rejected(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"openrouter",
		"nvidia",
		"huggingface",
		"opencode-go",
		"opencode-zen",
		"openai-codex",
		"commandcode-custom",
		"ollama-server",
		"lmstudio",
		"vllm",
		"alibaba-token-plan-intl",
		"cloudflare",
		"azure-openai",
		"snowflake-cortex",
		"databricks-ai",
		"infomaniak-ai",
		"vertex",
		"sagemaker",
		"oci-generative-ai",
		"watsonx",
		"sapaicore",
		"cohere",
		"replicate",
		"gitlab-duo",
		"nous-portal",
		"xai-oauth",
		"qwen-oauth",
		"minimax-oauth",
	}
	for _, id := range forbidden {
		if !isDedicatedProductOrFamily(id) {
			t.Fatalf("expected forbidden ID %q to be rejected by dedicated product guardrail", id)
		}
	}
}

func TestKimiCoding_OfflineMessagesAndStreamFixture(t *testing.T) {
	const testAPIKey = "kimi-test-secret-key"
	t.Setenv("KIMI_API_KEY", testAPIKey)

	var (
		receivedMessagesAuth atomic.Pointer[string]
		receivedUserAgent    atomic.Pointer[string]
		messagesHit          atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("x-api-key")
		ua := r.Header.Get("User-Agent")
		receivedMessagesAuth.Store(&apiKey)
		receivedUserAgent.Store(&ua)

		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/messages") && r.Method == http.MethodPost:
			messagesHit.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_kimi_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"k3\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from kimi\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// 1. Copy the embedded kimi-coding profile but set Endpoint.BaseURL to httptest server.
	embeddedKimi, err := providerprofiles.EmbeddedProfile("kimi-coding")
	if err != nil {
		t.Fatalf("EmbeddedProfile(kimi-coding): %v", err)
	}
	testKimi := embeddedKimi
	testKimi.Endpoint.BaseURL = srv.URL + "/coding/"

	testCatalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testKimi})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	// 2. Drive PrepareProviderProfilesWithCatalog + InstallStandardBundleOn + BuildBackendWithLifecycle.
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: kimi-coding\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "kimi-instance", Kind: standardplugins.ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}
	prepared, err := standardplugins.PrepareProviderProfilesWithCatalog(cfg, testCatalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		srv.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// 3. Verify static models (no network call needed)
	if backend.ModelInventory == nil {
		t.Fatal("expected non-nil ModelInventory on kimi backend")
	}
	snapshot, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	expectedModels := []string{"k3", "k3-256k", "kimi-for-coding", "kimi-for-coding-highspeed"}
	if len(snapshot.Models) != len(expectedModels) {
		t.Fatalf("kimi-coding model count=%d, want %d", len(snapshot.Models), len(expectedModels))
	}
	for i, m := range snapshot.Models {
		if m.NativeID != expectedModels[i] {
			t.Fatalf("kimi-coding model[%d]=%q, want %q", i, m.NativeID, expectedModels[i])
		}
	}

	// 4. Open streaming Anthropic messages call
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationAnthropicMessages,
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "kimi-instance", Model: "k3"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if messagesHit.Load() == 0 {
		t.Fatal("expected POST /v1/messages to be called on upstream server")
	}
	if collected.Text.String() != "hello from kimi" {
		t.Fatalf("decoded response text mismatch: got %q, want %q", collected.Text.String(), "hello from kimi")
	}

	// 5. Assert x-api-key was KIMI_API_KEY
	if auth := receivedMessagesAuth.Load(); auth == nil || *auth != testAPIKey {
		t.Fatalf("x-api-key mismatch: got %v, want %q", auth, testAPIKey)
	}

	// 6. Assert no client identifier spoofing (truthful AIProxer identity)
	if ua := receivedUserAgent.Load(); ua != nil {
		lowerUA := strings.ToLower(*ua)
		if strings.Contains(lowerUA, "claudecode") || strings.Contains(lowerUA, "opencode") {
			t.Fatalf("spoofed client identifier detected in User-Agent: %q", *ua)
		}
	}
}

func TestThinkingMachines_OfflineRequestPathAuthStreamFixture(t *testing.T) {
	const testAPIKey = "tinker-test-secret-key"
	t.Setenv("TINKER_API_KEY", testAPIKey)

	var (
		receivedMessagesAuth atomic.Pointer[string]
		messagesHit          atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("x-api-key")
		receivedMessagesAuth.Store(&apiKey)

		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/messages") && r.Method == http.MethodPost:
			messagesHit.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tinker_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"thinkingmachines/Inkling\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from tinker\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// 1. Copy the embedded thinking-machines profile but set Endpoint.BaseURL to httptest server.
	embeddedTM, err := providerprofiles.EmbeddedProfile("thinking-machines")
	if err != nil {
		t.Fatalf("EmbeddedProfile(thinking-machines): %v", err)
	}
	testTM := embeddedTM
	testTM.Endpoint.BaseURL = srv.URL + "/services/tinker-prod/anthropic/api"

	testCatalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testTM})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	// 2. Drive PrepareProviderProfilesWithCatalog + InstallStandardBundleOn + BuildBackendWithLifecycle.
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: thinking-machines\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "tinker-instance", Kind: standardplugins.ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}
	prepared, err := standardplugins.PrepareProviderProfilesWithCatalog(cfg, testCatalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		srv.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// 3. Verify static model identity
	if backend.ModelInventory == nil {
		t.Fatal("expected non-nil ModelInventory on tinker backend")
	}
	snapshot, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].NativeID != "thinkingmachines/Inkling" {
		t.Fatalf("thinking-machines model mismatch: %+v", snapshot.Models)
	}

	// 4. Open streaming Anthropic messages call
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationAnthropicMessages,
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "tinker-instance", Model: "thinkingmachines/Inkling"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if messagesHit.Load() == 0 {
		t.Fatal("expected POST /v1/messages to be called on upstream server")
	}
	if collected.Text.String() != "hello from tinker" {
		t.Fatalf("decoded response text mismatch: got %q, want %q", collected.Text.String(), "hello from tinker")
	}

	// 5. Assert x-api-key was TINKER_API_KEY
	if auth := receivedMessagesAuth.Load(); auth == nil || *auth != testAPIKey {
		t.Fatalf("x-api-key mismatch: got %v, want %q", auth, testAPIKey)
	}

	// 6. Assert reasoning_replay is disabled
	if !slices.Contains(embeddedTM.Capabilities.Disable, aipapi.CapabilityReasoningReplay) {
		t.Fatal("expected reasoning_replay to be disabled on thinking-machines")
	}
}
