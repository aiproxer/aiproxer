# Automated Dependency Updates

Automate minor/patch dependency updates to reduce maintenance burden and stay current with security fixes. This requires a solid CI pipeline — tests and linting must pass before any auto-merge.

## Dependabot

Use Dependabot (`.github/dependabot.yml`) for Go module and GitHub Actions updates. Group updates per dependency family so one incompatible family does not block the others. Dependabot does not run `go mod tidy` across every module in a multi-module repository; when sibling modules drift, synchronize them in a follow-up commit on the update branch.

## Auto-Merge Strategy

- **Minor and patch updates**: Auto-merge only after CI passes (tests + lint + govulncheck) and the package is low-risk for the project
- **Major updates**: Create PR for manual review (may contain breaking changes)
- **Security updates**: Prioritize review and mitigation. Apply the same compatibility, required-check, dependency-risk, and authorization gates; a security label does not authorize a breaking upgrade or bypass review.

For workflow configuration files (dependabot.yml and auto-merge), see the local `golang-continuous-integration` skill.

## Update Verification

Before committing a dependency update:

0. Changelogs may suggest improvements applicable to the project.
1. Run `go test ./...` and `go build ./...`
2. Scan with `govulncheck ./...` or `go tool govulncheck ./...`
3. Release notes/changelogs for libraries that affect persistence, serialization, networking, authentication, authorization, cryptography, or public APIs may contain important information about breaking changes
4. Major version upgrades may contain breaking changes — the package's changelog documents them
5. New APIs or patterns introduced in the updated version may offer improvements worth considering
