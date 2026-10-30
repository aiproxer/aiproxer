[CmdletBinding()]
param(
    [string]$RepositoryRoot
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = Split-Path -Parent $PSScriptRoot
}
$repositoryRootPath = [System.IO.Path]::GetFullPath($RepositoryRoot)
$catalogRoot = Join-Path $repositoryRootPath '.agents\skills'
$manifestPath = Join-Path $repositoryRootPath '.agents\catalog.json'
$errors = [System.Collections.Generic.List[string]]::new()

if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    Write-Error "Catalog manifest is missing: $manifestPath"
    exit 1
}

if (-not (Test-Path -LiteralPath $catalogRoot -PathType Container)) {
    Write-Error "Catalog directory is missing: $catalogRoot"
    exit 1
}

try {
    $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
}
catch {
    Write-Error "Catalog manifest is invalid JSON: $($_.Exception.Message)"
    exit 1
}

if ($manifest.version -ne 1) {
    $errors.Add("unsupported catalog version: $($manifest.version)")
}

$declaredNames = [System.Collections.Generic.List[string]]::new()
foreach ($group in $manifest.groups.PSObject.Properties) {
    foreach ($name in $group.Value) {
        $declaredNames.Add([string]$name)
    }
}

$goSkillNames = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
if ($null -ne $manifest.groups.golang) {
    foreach ($name in $manifest.groups.golang) {
        [void]$goSkillNames.Add([string]$name)
    }
}

$duplicateDeclarations = $declaredNames | Group-Object | Where-Object Count -gt 1
foreach ($duplicate in $duplicateDeclarations) {
    $errors.Add("duplicate catalog declaration: $($duplicate.Name)")
}

$declared = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
foreach ($name in $declaredNames) {
    [void]$declared.Add($name)
}

$actualNames = Get-ChildItem -LiteralPath $catalogRoot -Directory |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'SKILL.md') -PathType Leaf } |
    Select-Object -ExpandProperty Name

foreach ($name in $declared) {
    if ($actualNames -notcontains $name) {
        $errors.Add("declared skill is missing: $name")
    }
}

foreach ($name in $actualNames) {
    if (-not $declared.Contains($name)) {
        $errors.Add("undeclared skill directory: $name")
    }
}

$allowedFrontmatter = [System.Collections.Generic.HashSet[string]]::new(
    [string[]]@('name', 'description', 'license', 'metadata'),
    [System.StringComparer]::Ordinal
)

foreach ($name in $actualNames) {
    if ($name -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $name.Length -gt 64) {
        $errors.Add("invalid skill directory name: $name")
        continue
    }

    $skillPath = Join-Path (Join-Path $catalogRoot $name) 'SKILL.md'
    $lines = Get-Content -LiteralPath $skillPath
    if ($lines.Count -lt 4 -or $lines[0] -ne '---') {
        $errors.Add("missing YAML frontmatter: $name")
        continue
    }

    $closingMarker = -1
    for ($index = 1; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -eq '---') {
            $closingMarker = $index
            break
        }
    }

    if ($closingMarker -lt 2) {
        $errors.Add("unterminated YAML frontmatter: $name")
        continue
    }

    $fields = @{}
    for ($index = 1; $index -lt $closingMarker; $index++) {
        if ($lines[$index] -match '^([a-z][a-z0-9-]*):(?:\s*(.*))?$') {
            $field = $Matches[1]
            $value = $Matches[2]
            $fields[$field] = [pscustomobject]@{ Index = $index; Value = $value }
            if (-not $allowedFrontmatter.Contains($field)) {
                $errors.Add("unsupported frontmatter field '$field' in $name")
            }
        }
    }


    if ($goSkillNames.Contains($name)) {
        foreach ($field in $fields.Keys) {
            if ($field -notin @('name', 'description')) {
                $errors.Add("Go skill '$name' has non-portable frontmatter field '$field'")
            }
        }

        if ($lines.Count -gt 500) {
            $errors.Add("Go skill entrypoint exceeds 500 lines: $name ($($lines.Count))")
        }

        $skillRoot = Split-Path -Parent $skillPath
        $forbiddenPatterns = [ordered]@{
            'upstream skill package reference' = 'samber/cc-skills'
            'upstream override policy'         = 'Community default|company skill|company-specific|company default'
            'agent-specific orchestration'     = 'ultracode|sub-?agents?|Agent tool|EnterWorktree'
            'unrelated documentation service'  = 'Context7|DeepWiki|OpenDeep|zRead'
        }

        foreach ($file in Get-ChildItem -LiteralPath $skillRoot -Recurse -File) {
            if ($file.Extension -notin @('.md', '.json', '.yml', '.yaml', '.go')) {
                continue
            }

            $content = Get-Content -Raw -LiteralPath $file.FullName

            if ($file.Extension -eq '.json') {
                try {
                    $null = $content | ConvertFrom-Json
                }
                catch {
                    $relativePath = [System.IO.Path]::GetRelativePath($repositoryRootPath, $file.FullName)
                    $errors.Add("invalid JSON in Go skill file '$relativePath': $($_.Exception.Message)")
                }
            }

            if ($file.Extension -eq '.md' -and $file.FullName -notmatch '[\\/]assets[\\/]') {
                $proseContent = [regex]::Replace($content, '(?s)(?:```|~~~).*?(?:```|~~~)', '')
                $proseContent = [regex]::Replace($proseContent, '`[^`]*`', '')
                foreach ($match in [regex]::Matches($proseContent, '\[[^\]]+\]\(([^)]+)\)')) {
                    $target = $match.Groups[1].Value.Trim().Trim('<', '>')
                    if ($target -match '^(?:[a-z][a-z0-9+.-]*:|#)' -or $target -match '^/') {
                        continue
                    }

                    # Generic Go calls such as Invoke[T](value) are valid prose/code but
                    # resemble Markdown links. Real relative targets should not contain
                    # unescaped whitespace or argument separators.
                    if ($target -match '[\s,]') {
                        continue
                    }

                    $targetPath = ($target -split '#', 2)[0]
                    if ([string]::IsNullOrWhiteSpace($targetPath)) {
                        continue
                    }

                    $decodedPath = [System.Uri]::UnescapeDataString($targetPath)
                    $resolvedTarget = Join-Path $file.DirectoryName $decodedPath
                    if (-not (Test-Path -LiteralPath $resolvedTarget)) {
                        $relativePath = [System.IO.Path]::GetRelativePath($repositoryRootPath, $file.FullName)
                        $errors.Add("broken relative link in '$relativePath': $target")
                    }
                }
            }

            foreach ($entry in $forbiddenPatterns.GetEnumerator()) {
                if ($content -match $entry.Value) {
                    $relativePath = [System.IO.Path]::GetRelativePath($repositoryRootPath, $file.FullName)
                    $errors.Add("$($entry.Key) in Go skill file: $relativePath")
                }
            }
        }
    }

    if (-not $fields.ContainsKey('name')) {
        $errors.Add("missing frontmatter name: $name")
    }
    else {
        $frontmatterName = $fields.name.Value.Trim('"', "'")
        if ($frontmatterName -cne $name) {
            $errors.Add("frontmatter name '$frontmatterName' does not match directory '$name'")
        }
    }

    if (-not $fields.ContainsKey('description')) {
        $errors.Add("missing frontmatter description: $name")
    }
    else {
        $description = $fields.description.Value.Trim()
        # A colon followed by whitespace starts a YAML mapping unless the
        # description is quoted or uses a block scalar.
        if ($description -notmatch '^["''>|]' -and $description -match ':\s') {
            $errors.Add("unquoted YAML mapping separator in description: $name (quote the description or use a block scalar)")
        }
        $hasFoldedBody = $description -match '^[>|][+-]?$' -and
            ($fields.description.Index + 1) -lt $closingMarker -and
            $lines[$fields.description.Index + 1] -match '^\s+\S'
        if ([string]::IsNullOrWhiteSpace($description) -or
            (($description -match '^[>|][+-]?$') -and -not $hasFoldedBody)) {
            $errors.Add("empty frontmatter description: $name")
        }
    }
}

$legacyRoots = @(
    '.codex\skills',
    '.cursor\skills',
    '.kiro\skills',
    '.opencode\skills',
    '.pi\skills'
)

foreach ($relativeRoot in $legacyRoots) {
    $legacyRoot = Join-Path $repositoryRootPath $relativeRoot
    if (-not (Test-Path -LiteralPath $legacyRoot -PathType Container)) {
        continue
    }

    foreach ($name in $declared) {
        $legacySkill = Join-Path (Join-Path $legacyRoot $name) 'SKILL.md'
        if (Test-Path -LiteralPath $legacySkill -PathType Leaf) {
            $errors.Add("legacy duplicate for '$name': $legacySkill")
        }
    }
}

if ($errors.Count -gt 0) {
    foreach ($message in $errors) {
        [Console]::Error.WriteLine("ERROR: $message")
    }
    exit 1
}

"PASS: $($declared.Count) canonical agent skills; no legacy duplicates"
