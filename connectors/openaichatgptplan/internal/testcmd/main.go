// Command testcmd serves the openai-chatgpt-plan backend plugin with its
// Responses transport addressed to a LOCAL REFERENCE SERVER.
//
// It is the connector's TEST-ONLY executable. It is NOT the shipped command
// (cmd/aip-backend-openaichatgptplan), it is not named by the release metadata,
// and it cannot be selected by configuration: the shipped configuration surface
// has no endpoint key at all (ParseConfigYAML refuses responses_base_url), so
// production execution always sends to the documented public resource.
//
// The reference base URL reaches this binary out of band, through a file beside
// the executable that only the host test staging helper writes. The ABI
// serving, the descriptor and the connector service are the production ones;
// only the endpoint the staged host integration is built to observe differs.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// referenceBaseURLFileName names the file beside the staged executable that
// carries this test command's reference base URL. The production command never
// reads it.
const referenceBaseURLFileName = "responses-reference-base-url"

func main() {
	baseURL, err := referenceBaseURL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "openai-chatgpt-plan test connector: %v\n", err)
		os.Exit(1)
	}
	connector, err := service.NewWithReferenceEndpoint(baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openai-chatgpt-plan test connector: %v\n", err)
		os.Exit(1)
	}
	backendplugin.Main("aip-backend-openaichatgptplan", connector)
}

// referenceBaseURL reads the reference endpoint the staging helper wrote beside
// this executable.
//
// There is no fallback: a test connector without its reference endpoint would
// silently call the public provider, which is exactly what a staged test must
// never do.
func referenceBaseURL() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve the staged test executable: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(executable), referenceBaseURLFileName))
	if err != nil {
		return "", fmt.Errorf("read the reference endpoint beside the staged test executable: %w", err)
	}
	baseURL := strings.TrimSpace(string(raw))
	if baseURL == "" {
		return "", fmt.Errorf("the reference endpoint beside the staged test executable is empty")
	}
	return baseURL, nil
}
