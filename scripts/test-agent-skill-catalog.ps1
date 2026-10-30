[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$checker = Join-Path $PSScriptRoot 'check-agent-skill-catalog.ps1'

if (-not (Test-Path -LiteralPath $checker -PathType Leaf)) {
    throw "Catalog checker is missing: $checker"
}

$temporaryBase = [System.IO.Path]::GetTempPath()
$fixtureRoot = Join-Path $temporaryBase ("aip-agent-skill-catalog-test-{0}" -f [guid]::NewGuid())
$resolvedTemporaryBase = [System.IO.Path]::GetFullPath($temporaryBase)
$resolvedFixtureRoot = [System.IO.Path]::GetFullPath($fixtureRoot)

if (-not $resolvedFixtureRoot.StartsWith($resolvedTemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Fixture path escapes the temporary directory: $resolvedFixtureRoot"
}

function Invoke-CatalogCheck {
    param([string]$Root)

    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = & pwsh -NoProfile -File $checker -RepositoryRoot $Root 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorActionPreference
    return [pscustomobject]@{
        ExitCode = $exitCode
        Output   = ($output -join [Environment]::NewLine)
    }
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content)

    [System.IO.File]::WriteAllText($Path, $Content, [System.Text.UTF8Encoding]::new($false))
}

try {
    $skillDirectory = Join-Path $fixtureRoot '.agents\skills\example-skill'
    New-Item -ItemType Directory -Path $skillDirectory -Force | Out-Null

    Write-Utf8NoBom -Path (Join-Path $fixtureRoot '.agents\catalog.json') -Content @'
{
  "version": 1,
  "groups": {
    "golang": [
      "example-skill"
    ]
  }
}
'@

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
---

# Example
'@

    $valid = Invoke-CatalogCheck -Root $fixtureRoot
    if ($valid.ExitCode -ne 0) {
        throw "Valid catalog was rejected:`n$($valid.Output)"
    }

    $duplicateDirectory = Join-Path $fixtureRoot '.codex\skills\example-skill'
    New-Item -ItemType Directory -Path $duplicateDirectory -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $skillDirectory 'SKILL.md') -Destination (Join-Path $duplicateDirectory 'SKILL.md')

    $duplicate = Invoke-CatalogCheck -Root $fixtureRoot
    if ($duplicate.ExitCode -eq 0 -or $duplicate.Output -notmatch 'legacy duplicate') {
        throw "Legacy duplicate was not rejected:`n$($duplicate.Output)"
    }

    Remove-Item -LiteralPath (Join-Path $fixtureRoot '.codex') -Recurse -Force

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
allowed-tools: Read
---

# Example
'@

    $unsupported = Invoke-CatalogCheck -Root $fixtureRoot
    if ($unsupported.ExitCode -eq 0 -or $unsupported.Output -notmatch 'unsupported frontmatter') {
        throw "Unsupported frontmatter was not rejected:`n$($unsupported.Output)"
    }

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
---

# Example

Load `samber/cc-skills-golang@example`.
'@

    $upstreamReference = Invoke-CatalogCheck -Root $fixtureRoot
    if ($upstreamReference.ExitCode -eq 0 -or $upstreamReference.Output -notmatch 'upstream skill package reference') {
        throw "Upstream package reference was not rejected:`n$($upstreamReference.Output)"
    }

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
---

# Example

Read [missing](references/missing.md).
'@

    $brokenLink = Invoke-CatalogCheck -Root $fixtureRoot
    if ($brokenLink.ExitCode -eq 0 -or $brokenLink.Output -notmatch 'broken relative link') {
        throw "Broken relative link was not rejected:`n$($brokenLink.Output)"
    }

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
---

# Example
'@
    $evalDirectory = Join-Path $skillDirectory 'evals'
    New-Item -ItemType Directory -Path $evalDirectory -Force | Out-Null
    Write-Utf8NoBom -Path (Join-Path $evalDirectory 'evals.json') -Content '{invalid'

    $invalidJson = Invoke-CatalogCheck -Root $fixtureRoot
    if ($invalidJson.ExitCode -eq 0 -or $invalidJson.Output -notmatch 'invalid JSON') {
        throw "Invalid skill JSON was not rejected:`n$($invalidJson.Output)"
    }

    "PASS: agent skill catalog validator"
}
finally {
    if (Test-Path -LiteralPath $fixtureRoot) {
        Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
    }
}
