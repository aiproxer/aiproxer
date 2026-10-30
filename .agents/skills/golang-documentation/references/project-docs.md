# Project Documentation

See the local `golang-continuous-integration` skill for release workflow considerations.

## README.md

A license is required when the project is distributed, subject to the repository's policy. A README is the front page of your project. Make it simple, clear, and scannable.

### Section Order

Adapt these sections and their order to the audience and existing repository docs:

1. **Title** — project name as `# heading`
2. **Summary** — 1-2 sentences explaining what the project does
3. **Demo** — code snippet (libraries), GIF/video (CLIs), or screenshot (web UIs)
4. **Getting Started** — installation + minimal working example
5. **Features / Specification** — the longest section, organized by feature area
6. **Contributing** — link to CONTRIBUTING.md or inline if very short
7. **License** — license name + link

---

## CONTRIBUTING.md

Document a reproducible path from checkout to a focused check. Investigate setup friction when it prevents contributors from validating changes; propose tooling only when its maintenance cost is justified.

### Setup friction

Consider these remedies when the corresponding problem is demonstrated:

| Problem | Solution |
| --- | --- |
| Complex build steps | Add a `Makefile` with `make build`, `make test`, `make lint` |
| External service dependencies | Add `docker-compose.yml` for local dev |
| Inconsistent dev environments | Add `.devcontainer/` for VS Code devcontainers |
| Slow test suite | Separate unit tests (fast) from integration tests (build tags) |
| Missing documentation | Add `make help` that lists available targets |

---

## Changelog

Update a changelog when the project's release policy calls for one. Track notable changes for each release.

### Format

```markdown
## [1.2.0] - 2026-03-08

### Added

- New `WithTimeout` option for client configuration

### Changed

- Improved retry logic to use exponential backoff

### Fixed

- Race condition in connection pool under heavy load

### Deprecated

- `SetTimeout()` method — use `WithTimeout()` option instead

[1.2.0]: https://github.com/{owner}/{repo}/compare/v1.1.0...v1.2.0
```

### Change Categories

- **Added** — new features
- **Changed** — changes in existing functionality
- **Deprecated** — features that will be removed
- **Removed** — removed features
- **Fixed** — bug fixes
- **Security** — vulnerability fixes

### GitHub Releases as Alternative

For simpler projects, GitHub Releases can replace a CHANGELOG file. GoReleaser auto-generates release notes from git commits.

---

## Distribution

Offer installation paths that match the project and its users (for example binaries, containers, a package manager, or source). Do not document paths that are not maintained. Because:

- Each installation method eliminates friction for a different user segment
- Users adopt tools that fit their workflow, not tools that force workflow changes
- Tools users _want to_ use spread faster than tools users _have to_ accommodate

### Dockerfile Best Practices

Use multi-stage builds with a minimal final image:

```dockerfile
# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary ./cmd/server

# Final stage
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/binary /binary
ENTRYPOINT ["/binary"]
```
