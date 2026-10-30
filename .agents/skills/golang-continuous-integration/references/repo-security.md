# Repository security review

Inspect the existing repository policy when CI or release permissions are in scope. Report demonstrated gaps with the affected event, token, code source, and consequence. For a narrow workflow review, recommend only the settings that control its trust boundary; repository settings are owner policy and changing workflow files does not authorize changing them.

## Merge and release gates

Required status checks, review requirements, protected refs, rulesets, and release environments must match the project's contribution and release policy. A required job that is skipped by path/event filters can leave a PR pending or create an enforcement gap; check the actual check names and every merge path. Account for administrator/bot bypasses when they affect the gate under review. Environment approvals are useful where publishing needs human authorization; they are not a universal prerequisite for every release.

Auto-merge waits for the checks and approvals that protection actually requires. Verify that auto-merge is enabled and that the bot cannot bypass those gates. Allow only explicitly approved update types and dependencies; empty or unexpected metadata should fail closed. Security updates still require compatibility review. See [GitHub's Dependabot automation](https://docs.github.com/en/code-security/tutorials/secure-your-dependencies/automate-dependabot-with-actions).

## Workflow trust boundary

Start with `permissions: contents: read` or `{}` and elevate only the job that needs the operation. Fork and Dependabot event policies may reduce token permissions and secret access despite a workflow declaration; inspect the actual trigger and repository policy when diagnosing a failure. Keep a privileged metadata-only job free of PR-head checkouts, scripts, dependency installation, caches, and artifacts containing untrusted executable content. `pull_request_target` is appropriate only when this separation holds; executing PR code there creates a privileged trust-boundary violation.

Never interpolate an untrusted title, body, branch name, or other event text directly into shell source. Pass data through a quoted environment variable or structured argument. Pin actions to reviewed full commit SHAs. A pin does not pin tools/images that the action downloads; check those inputs separately when reproducibility is part of the contract.

For public contributions, workflow-run approval controls resource use; they do not make submitted code trusted or grant it ordinary repository secrets. Check self-hosted runner isolation and persistence when untrusted jobs can reach them.

## Credentials and artifacts

Use repository/environment secrets or an explicitly scoped identity federation policy. Bind OIDC trust to the intended repository, ref and environment. Privileged publish jobs must consume the intended tested source/artifact and validate its provenance rather than trusting a PR artifact or cache. Logs, coverage, profiles and uploaded artifacts may contain secrets or private data; review retained content and access.

## Settings links

When a demonstrated finding requires owner action, derive links from the actual Git remote:

- Rulesets/branch protection: `https://github.com/{owner}/{repo}/settings/rules` or `/settings/branches`.
- Actions permissions: `https://github.com/{owner}/{repo}/settings/actions`.
- Secrets: `https://github.com/{owner}/{repo}/settings/secrets/actions`.
- Environments: `https://github.com/{owner}/{repo}/settings/environments`.

Sources: [GitHub Actions security reference](https://docs.github.com/en/actions/reference/security/secure-use), [Dependabot Actions troubleshooting](https://docs.github.com/en/code-security/dependabot/troubleshooting-dependabot/troubleshooting-dependabot-on-github-actions).
