package qa

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestQAFastPreflight_TestCostAnchorHasStableCompilerPath(t *testing.T) {
	t.Parallel()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is required to execute the Windows anchor path helper")
	}
	root := t.TempDir()
	command := `$ErrorActionPreference = 'Stop'
$ast = [Management.Automation.Language.Parser]::ParseFile($env:AIP_TEST_COST_SCRIPT, [ref]$null, [ref]$null)
$node = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Get-AnchorWorktreeRoot' }, $true)
if ($null -eq $node) { throw 'missing stable anchor path helper' }
. ([ScriptBlock]::Create($node.Extent.Text))
$paths = @(
    (Get-AnchorWorktreeRoot $env:AIP_TEST_HEAD_ROOT 'bb1ef9620ee6e8d9199950161e46fc51914945f2'),
    (Get-AnchorWorktreeRoot $env:AIP_TEST_HEAD_ROOT 'bb1ef9620ee6e8d9199950161e46fc51914945f2'),
    (Get-AnchorWorktreeRoot $env:AIP_TEST_HEAD_ROOT '6dbb831885341516117034923f0c3203373aded0')
)
ConvertTo-Json -InputObject $paths -Compress`
	cmd := exec.CommandContext(t.Context(), pwsh, "-NoLogo", "-NoProfile", "-Command", command)
	cmd.Env = append(os.Environ(), "AIP_TEST_COST_SCRIPT="+repositoryFile(t, "scripts", "test-cost-ratchet.ps1"), "AIP_TEST_HEAD_ROOT="+filepath.Join(root, "head"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stable anchor path: %v\n%s", err, out)
	}
	var paths []string
	if err := json.Unmarshal(out, &paths); err != nil {
		t.Fatalf("decode anchor paths: %v\n%s", err, out)
	}
	want := []string{filepath.Join(root, "aip-testcost-anchor-bb1ef962"), filepath.Join(root, "aip-testcost-anchor-bb1ef962"), filepath.Join(root, "aip-testcost-anchor-6dbb8318")}
	if len(paths) != len(want) {
		t.Fatalf("anchor paths = %q, want %q", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("anchor path %d = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestQAFastPreflight_TestCostPolicyUpdateAuthorization(t *testing.T) {
	t.Parallel()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is required to execute the Windows policy guard")
	}
	command := `$ErrorActionPreference = 'Stop'
$ast = [Management.Automation.Language.Parser]::ParseFile($env:AIP_TEST_COST_SCRIPT, [ref]$null, [ref]$null)
$node = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Test-PolicyProtection' }, $true)
if ($null -eq $node) { throw 'missing policy protection function' }
. ([ScriptBlock]::Create($node.Extent.Text))
$PolicyRelativePath = 'scripts/test-cost-budget.json'
function Resolve-Commit { return 'base' }
function Test-PathAtCommit { return $true }
function git { $global:LASTEXITCODE = 1 }
try {
    Test-PolicyProtection -RepositoryRoot 'fixture' -HeadCommit 'head' -BaseRevision 'base' -AllowGrowth $false -AllowPolicyUpdate $false
    throw 'unauthorized policy change was accepted'
} catch {
    if ($_ -notmatch 'budget policy changed') { throw }
}
Test-PolicyProtection -RepositoryRoot 'fixture' -HeadCommit 'head' -BaseRevision 'base' -AllowGrowth $false -AllowPolicyUpdate $true
Test-PolicyProtection -RepositoryRoot 'fixture' -HeadCommit 'head' -BaseRevision 'base' -AllowGrowth $true -AllowPolicyUpdate $false`
	cmd := exec.CommandContext(t.Context(), pwsh, "-NoLogo", "-NoProfile", "-Command", command)
	cmd.Env = append(os.Environ(), "AIP_TEST_COST_SCRIPT="+repositoryFile(t, "scripts", "test-cost-ratchet.ps1"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("policy-only authorization must preserve the explicit guard: %v\n%s", err, out)
	}
}

func TestQAFastPreflight_TestCostFrozenObservabilityCompatibility(t *testing.T) {
	t.Parallel()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is required to execute the Windows anchor compatibility helper")
	}
	const path = "internal/core/runtime/cancellation_observability_phase6_test.go"
	const old = "CancelTimeout: 50 * time.Millisecond"
	for _, tc := range []struct {
		name, anchor string
		guards       int
		changed, bad bool
	}{
		{name: "known fixture", anchor: "bb1ef9620ee6e8d9199950161e46fc51914945f2", guards: 5, changed: true},
		{name: "drift fails closed", anchor: "bb1ef9620ee6e8d9199950161e46fc51914945f2", guards: 4, bad: true},
		{name: "unrelated anchor", anchor: strings.Repeat("a", 40), guards: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=QA", "-c", "user.email=qa@example.com", "-c", "commit.gpgsign=false"}, args...)...)
				// root is a throwaway fixture repository. Git exports GIT_DIR to every
				// hook it runs, so an inherited GIT_DIR would initialise and commit
				// inside the real repository, destroying its index and refs.
				cmd.Env = gitscope.Environ()
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			file := filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			original := strings.Repeat(old+"\n", tc.guards)
			if err := os.WriteFile(file, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			git("commit", "-qm", "base")
			before := git("rev-parse", "HEAD")
			// Load the actual helper and its Git operations through the parser,
			// without executing the ratchet's top-level measurement workflow.
			command := `$ErrorActionPreference = 'Stop'
$ast = [Management.Automation.Language.Parser]::ParseFile($env:AIP_TEST_COST_SCRIPT, [ref]$null, [ref]$null)
$names = @('Get-GitText', 'Invoke-GitChecked', 'Test-CleanCheckout', 'Apply-AnchorCompatibilityPatch')
foreach ($name in $names) {
    $node = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name }, $true)
    if ($null -eq $node) { throw "missing function: $name" }
    . ([ScriptBlock]::Create($node.Extent.Text))
}
Apply-AnchorCompatibilityPatch -RepositoryRoot $env:AIP_TEST_ANCHOR_ROOT -AnchorRoot $env:AIP_TEST_ANCHOR_ROOT -AnchorCommit $env:AIP_TEST_ANCHOR_SHA -TempRoot $env:AIP_TEST_ANCHOR_ROOT`
			cmd := exec.CommandContext(t.Context(), pwsh, "-NoLogo", "-NoProfile", "-Command", command)
			// The compatibility helper runs `git -C $AnchorRoot add/apply/commit`
			// against the fixture anchor, so it must not inherit an ambient GIT_DIR.
			cmd.Env = append(gitscope.Environ(), "AIP_TEST_COST_SCRIPT="+repositoryFile(t, "scripts", "test-cost-ratchet.ps1"), "AIP_TEST_ANCHOR_ROOT="+root, "AIP_TEST_ANCHOR_SHA="+tc.anchor)
			out, err := cmd.CombinedOutput()
			if tc.bad {
				if err == nil || !strings.Contains(string(out), "expected five known cancellation guards") {
					t.Fatalf("drift must fail closed: %v\n%s", err, out)
				}
			} else if err != nil {
				t.Fatalf("anchor compatibility: %v\n%s", err, out)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			want := original
			if tc.changed {
				want = strings.ReplaceAll(original, old, "CancelTimeout: time.Second")
				if changed := git("diff", "--name-only", before, "HEAD"); changed != path {
					t.Fatalf("compatibility changed %q, want only the frozen test fixture", changed)
				}
			} else if git("rev-parse", "HEAD") != before {
				t.Fatal("unrecognized or drifted anchor must not create a commit")
			}
			if string(got) != want || git("status", "--porcelain") != "" {
				t.Fatal("compatibility must preserve all other content and leave a clean checkout")
			}
		})
	}
}
