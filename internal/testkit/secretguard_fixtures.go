package testkit

// Synthetic secret fixtures for secrets-guard acceptance and contract tests.
// These are not production or customer credentials. Tests must never print these
// values (or meaningful substrings) in failure messages, logs, or error strings.

const (
	// SyntheticOpenAIAPIKey is an OpenAI-shaped placeholder for catalog/matcher tests.
	SyntheticOpenAIAPIKey = "sk-test-openai-secretguard-fixture-001" // #nosec G101 -- synthetic fixture only.

	// SyntheticOpenRouterAPIKey is an OpenRouter-shaped placeholder.
	SyntheticOpenRouterAPIKey = "sk-or-test-secretguard-fixture-002" // #nosec G101 -- synthetic fixture only.

	// SyntheticAnthropicSecretGuardKey is an Anthropic-shaped placeholder distinct from SyntheticAnthropicAPIKey.
	SyntheticAnthropicSecretGuardKey = "sk-ant-test-secretguard-fixture-003" // #nosec G101 -- synthetic fixture only.

	// SyntheticGeminiAPIKey is a Gemini/Google-shaped placeholder.
	SyntheticGeminiAPIKey = "AIzaSyTestSecretGuardFixture0004xxxx" // #nosec G101 -- synthetic fixture only.

	// SyntheticBearerCredential is a multi-user auth credential placeholder (request-scoped matcher only).
	SyntheticBearerCredential = "aip-mu-test-secretguard-bearer-005" // #nosec G101 -- synthetic fixture only.

	// SyntheticShortSecret is intentionally below the default min_secret_bytes (8) for exclusion tests.
	SyntheticShortSecret = "short" // #nosec G101 -- synthetic fixture only.

	// SyntheticOverlapLonger is the longer of two overlapping synthetic values (longest-match wins).
	SyntheticOverlapLonger = "secretguard-overlap-longer-value-aa" // #nosec G101 -- synthetic fixture only.

	// SyntheticOverlapShorter is a proper prefix of SyntheticOverlapLonger.
	SyntheticOverlapShorter = "secretguard-overlap" // #nosec G101 -- synthetic fixture only.

	// SyntheticUnicodeSecret embeds non-ASCII bytes for UTF-8 length preservation tests.
	SyntheticUnicodeSecret = "sg-ünîcode-секрет-006" // #nosec G101 -- synthetic fixture only.

	// SyntheticDuplicateValueAliasA and SyntheticDuplicateValueAliasB share the same value under different names.
	SyntheticDuplicateValueAliasA = "sg-dup-shared-value-fixture-007" // #nosec G101 -- synthetic fixture only.
	SyntheticDuplicateValueAliasB = SyntheticDuplicateValueAliasA

	// Provider-shaped detector fixtures use documented token syntax with synthetic values.
	SyntheticOpenAIDetectorKey    = "sk-" + "aB3dE5fG7hI9jK1mN3pQT3BlbkFJzX8cV6bN4mL2qR0sYtUv"                                                     // #nosec G101 -- synthetic fixture only.
	SyntheticAnthropicDetectorKey = "sk-ant-api03-abc123xyz-456def789ghij-klmnopqrstuvwx-3456yza789bcde-1234fghijklmnopby56aaaogaopaaaabc123xyzAA" // #nosec G101 -- synthetic fixture only.
	SyntheticGitHubPAT            = "ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"                                                                     // #nosec G101 -- synthetic fixture only.
	SyntheticSlackBotToken        = "xoxb-" + "781236542736-2364535789652-GkwFDQoHqzXDVsC6GzqYUypD"                                                // #nosec G101 -- synthetic fixture only.
	SyntheticStripeTestKey        = "sk_test_51qA7bC9dE2fG4hJ6kL8mN0pR2sT4uV6wX8yZ0aB2cD4"                                                         // #nosec G101 -- synthetic fixture only.

	// Composite and generic detector fixtures cover multipart and contextual rules.
	SyntheticAWSAccessKeyID        = "AKIALALEMEL33243OLIA"                                                                                                                               // #nosec G101 -- synthetic fixture only.
	SyntheticAWSSecretAccessKey    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"                                                                                                           // #nosec G101 -- synthetic fixture only.
	SyntheticGenericAPIKey         = "Zf3D0LXCM3EIMbgJpUNnkRtOfOueHznB"                                                                                                                   // #nosec G101 -- synthetic fixture only.
	SyntheticGenericPassword       = "g4F!mQ8#vZ2@rT6$xK9"                                                                                                                                // #nosec G101 -- synthetic fixture only.
	SyntheticGenericDetectorAPIKey = "dafa7817-e246-48f3-91a7-e87653d587b8"                                                                                                               // #nosec G101 -- synthetic fixture only.
	SyntheticCredentialURISecret   = "q9V7nB2K4xL8"                                                                                                                                       // #nosec G101 -- synthetic fixture only.
	SyntheticPrivateKey            = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQDAC4AWkdwKYSd8\nKs14IReLcYgADhoXk56ZzXI=\n-----END PRIVATE KEY-----" // #nosec G101 -- synthetic fixture only.
)

// SyntheticSecretGuardCorpusEntry describes a detector acceptance fixture.
// Material is test input only and must never be included in a failure message.
type SyntheticSecretGuardCorpusEntry struct {
	Name     string
	Value    string
	Material string
	RuleID   string
}

// SyntheticSecretGuardCorpus returns the positive detector cases used by the
// secret-guard parity matrix. Every entry is synthetic and deterministic.
func SyntheticSecretGuardCorpus() []SyntheticSecretGuardCorpusEntry {
	return []SyntheticSecretGuardCorpusEntry{
		{Name: "openai", Value: SyntheticOpenAIDetectorKey, Material: "OPENAI_API_KEY=" + SyntheticOpenAIDetectorKey, RuleID: "openai-api-key"},
		{Name: "anthropic", Value: SyntheticAnthropicDetectorKey, Material: "ANTHROPIC_API_KEY=" + SyntheticAnthropicDetectorKey, RuleID: "anthropic-api-key"},
		{Name: "github", Value: SyntheticGitHubPAT, Material: "GITHUB_TOKEN=" + SyntheticGitHubPAT, RuleID: "github-pat"},
		{Name: "slack", Value: SyntheticSlackBotToken, Material: "SLACK_BOT_TOKEN=" + SyntheticSlackBotToken, RuleID: "slack-bot-token"},
		{Name: "stripe", Value: SyntheticStripeTestKey, Material: "STRIPE_SECRET_KEY=" + SyntheticStripeTestKey, RuleID: "stripe-access-token"},
		{Name: "aws-multipart", Value: SyntheticAWSAccessKeyID, Material: "aws_token = \"" + SyntheticAWSAccessKeyID + "\" aws_secret_access_key = \"" + SyntheticAWSSecretAccessKey + "\"", RuleID: "aws-access-token"},
		{Name: "generic-api-key", Value: SyntheticGenericDetectorAPIKey, Material: "api_token = \"" + SyntheticGenericDetectorAPIKey + "\"", RuleID: "generic-api-key"},
		{Name: "generic-password", Value: SyntheticGenericPassword, Material: "login(user, \"" + SyntheticGenericPassword + "\")", RuleID: "generic-password"},
		{Name: "credential-uri", Value: SyntheticCredentialURISecret, Material: "DATABASE_URL=postgresql://app:" + SyntheticCredentialURISecret + "@db.internal/app", RuleID: "generic-credential-uri"},
		{Name: "private-key", Value: SyntheticPrivateKey, Material: SyntheticPrivateKey, RuleID: "private-key"},
	}
}

// SyntheticSecretGuardEnvNames are safe environment-variable *names* used in catalog tests.
// Values are never asserted by printing; use the Synthetic* constants above.
var SyntheticSecretGuardEnvNames = []string{
	"OPENAI_API_KEY",
	"OPENAI_API_KEY_2",
	"OPENAI_API_KEY_7",
	"OPENROUTER_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"AIP_TEST_SECRETGUARD_INCLUDE",
}

// AllSyntheticSecretGuardValues returns every synthetic secret string used by secrets-guard tests.
// Callers should scan logs/errors for these values and fail on any occurrence outside the private matcher.
func AllSyntheticSecretGuardValues() []string {
	return []string{
		SyntheticOpenAIAPIKey,
		SyntheticOpenRouterAPIKey,
		SyntheticAnthropicSecretGuardKey,
		SyntheticGeminiAPIKey,
		SyntheticBearerCredential,
		SyntheticShortSecret,
		SyntheticOverlapLonger,
		SyntheticOverlapShorter,
		SyntheticUnicodeSecret,
		SyntheticDuplicateValueAliasA,
		SyntheticOpenAIDetectorKey,
		SyntheticAnthropicDetectorKey,
		SyntheticGitHubPAT,
		SyntheticSlackBotToken,
		SyntheticStripeTestKey,
		SyntheticAWSAccessKeyID,
		SyntheticAWSSecretAccessKey,
		SyntheticGenericAPIKey,
		SyntheticGenericDetectorAPIKey,
		SyntheticGenericPassword,
		SyntheticCredentialURISecret,
		SyntheticPrivateKey,
	}
}

// AllSyntheticSecretGuardNeedles returns meaningful substrings that should also
// never appear outside the private matcher. These are shorter than the full
// fixture values so regression checks catch partial leaks.
func AllSyntheticSecretGuardNeedles() []string {
	return []string{
		"secretguard-fixture-001",
		"secretguard-fixture-002",
		"secretguard-fixture-003",
		"TestSecretGuardFixture0004",
		"secretguard-bearer-005",
		"secretguard-overlap",
		"sg-ünîcode",
		"dup-shared-value-fixture-007",
		"T3BlbkFJ",
		"sk-ant-api03",
		"ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5",
		"xoxb-781236542736",
		"sk_test_51qA7bC9dE2fG4hJ6kL8mN0pR2sT4uV6wX8yZ0aB2cD4",
		"AKIALALEMEL33243OLIA",
		"wJalrXUtnFEMI",
		"dafa7817-e246-48f3-91a7-e87653d587b8",
		"g4F!mQ8#vZ2@rT6$xK9",
		"q9V7nB2K4xL8",
		"BEGIN PRIVATE KEY",
	}
}
