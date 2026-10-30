package backendplugin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func validCredentialContract() backendplugin.CredentialContract {
	return backendplugin.CredentialContract{
		Version:     backendplugin.CredentialContractVersion,
		Class:       backendplugin.CredentialClassAPIKey,
		Source:      backendplugin.CredentialSourceHostEnv,
		EnvRoot:     "OPENROUTER_API_KEY",
		Slot:        backendplugin.CredentialSlotAPIKey,
		Selection:   backendplugin.CredentialSelectionQuotaPreOutput,
		QuotaDomain: "openrouter",
	}
}

func TestCredentialContractValidate_AcceptsWellFormed(t *testing.T) {
	t.Parallel()
	if err := validCredentialContract().Validate(); err != nil {
		t.Fatalf("well-formed contract rejected: %v", err)
	}
	tokenContract := validCredentialContract()
	tokenContract.Class = backendplugin.CredentialClassAPIToken
	tokenContract.Slot = backendplugin.CredentialSlotAPIToken
	tokenContract.EnvRoot = "CLOUDFLARE_API_TOKEN"
	tokenContract.QuotaDomain = "cloudflare"
	if err := tokenContract.Validate(); err != nil {
		t.Fatalf("well-formed token contract rejected: %v", err)
	}
}

func TestCredentialContractValidate_RejectsClosedEnumViolations(t *testing.T) {
	t.Parallel()
	base := validCredentialContract()
	for _, tc := range []struct {
		name   string
		mutate func(*backendplugin.CredentialContract)
	}{
		{"bad version", func(c *backendplugin.CredentialContract) { c.Version = 1 }},
		{"zero version", func(c *backendplugin.CredentialContract) { c.Version = 0 }},
		{"unknown class", func(c *backendplugin.CredentialContract) { c.Class = "bearer" }},
		{"empty class", func(c *backendplugin.CredentialContract) { c.Class = "" }},
		{"unknown source", func(c *backendplugin.CredentialContract) { c.Source = "child_env" }},
		{"file source", func(c *backendplugin.CredentialContract) { c.Source = "file" }},
		{"yaml source", func(c *backendplugin.CredentialContract) { c.Source = "yaml" }},
		{"empty source", func(c *backendplugin.CredentialContract) { c.Source = "" }},
		{"unknown slot", func(c *backendplugin.CredentialContract) { c.Slot = "token" }},
		{"empty slot", func(c *backendplugin.CredentialContract) { c.Slot = "" }},
		{"unknown selection", func(c *backendplugin.CredentialContract) { c.Selection = "round_robin" }},
		{"empty selection", func(c *backendplugin.CredentialContract) { c.Selection = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := base
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("invalid contract accepted")
			}
			if !errors.Is(err, backendplugin.ErrInvalidCredentialContract) {
				t.Fatalf("want %v, got %v", backendplugin.ErrInvalidCredentialContract, err)
			}
		})
	}
}

func TestCredentialContractValidate_RejectsBadEnvRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		root string
	}{
		{"empty", ""},
		{"lowercase", "openai_api_key"},
		{"leading digit", "1OPENAI_API_KEY"},
		{"numbered slot suffix is not a root", "OPENAI_API_KEY_2"},
		{"slot one suffix is invalid", "OPENAI_API_KEY_1"},
		{"spaces", "OPENAI API KEY"},
		{"dash", "OPENAI-API-KEY"},
		{"secret value with spaces", "sk-live secret value"},
		{"interpolation", "${OPENAI_API_KEY}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validCredentialContract()
			c.EnvRoot = tc.root
			if err := c.Validate(); err == nil {
				t.Fatalf("env root %q accepted", tc.root)
			}
		})
	}
}

func TestCredentialContractValidate_RejectsEmptyQuotaDomain(t *testing.T) {
	t.Parallel()
	c := validCredentialContract()
	c.QuotaDomain = "   "
	if err := c.Validate(); err == nil {
		t.Fatal("empty quota domain accepted")
	}
}

func TestCredentialContractValidate_ErrorsAreSecretFree(t *testing.T) {
	t.Parallel()
	c := validCredentialContract()
	c.EnvRoot = "not a root!"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error must never echo secret material: %v", err)
	}
}
