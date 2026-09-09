# User Guide

Welcome to the LLM Interactive Proxy User Guide. This guide provides comprehensive documentation for end-users who want to use and configure the proxy.

## Getting Started

- **[Quick Start Guide](quick-start.md)** - Get up and running in minutes with installation, basic configuration, and first steps
- **[Configuration Guide](configuration.md)** - Learn about configuration methods, precedence, and common scenarios
- **[Access Modes](access-modes.md)** - Single-user vs multi-user access mode behavior and configuration
- **[CLI Parameters Reference](cli-parameters.md)** - Complete reference for all CLI arguments and environment variables
- **[Database Configuration](database-configuration.md)** - Database setup for SQLite (default) and PostgreSQL

## Features

Advanced features that enhance the proxy's capabilities:

### Security Features

- **[Outbound URL safety](features/outbound-url-safety.md)** - SSRF-style checks on SSO, model registry, and redirect-following HTTP clients
- **[SSO Identity Provider Overview](sso-idp-overview.md)** - Overview of supported Identity Providers and configuration
- **[Quality Verifier System](features/quality-verifier.md)** - Real-time response verification using a secondary model
- **[Tool Access Control](features/tool-access-control.md)** - Fine-grained control over which tools models can access
- **[Dangerous Command Protection](features/dangerous-command-protection.md)** - Prevent execution of potentially harmful commands
- **[Dangerous Command Protection (Dev Tools)](features/dangerous-command-protection-dev-tools.md)** - Explain safe developer tool exemptions
- **[File Access Sandboxing](features/file-sandboxing.md)** - Restrict file system access to specific directories

### Single Sign-On (SSO)

- **[SSO Agent Setup](sso-agent-setup.md)** - Setting up SSO with agent integrations
- **[SSO Authentication](sso-authentication.md)** - Authentication flow details
- **[SSO Authorization](sso-authorization.md)** - Authorization modes and configuration
- **[SSO Configuration](sso-configuration.md)** - Detailed SSO configuration guide
- **[SSO Identity Provider Overview](sso-idp-overview.md)** - Overview of supported Identity Providers
- **[SSO Identity Provider Setup](sso-idp-setup.md)** - Setting up specific Identity Providers
- **[SSO Security](sso-security.md)** - Security considerations and best practices
- **[SSO Troubleshooting](sso-troubleshooting.md)** - Common issues and solutions

### Model Management

- **[Hybrid Backend](features/hybrid-backend.md)** - Use two models in sequence for reasoning and execution phases (experimental)
- **[Model Name Rewrites](features/model-name-rewrites.md)** - Transform model names dynamically with aliases and patterns
- **[URI Model Parameters](features/uri-model-parameters.md)** - Specify model parameters directly in model name strings
- **[Planning Phase Overrides](features/planning-phase.md)** - Use stronger models for planning phases in coding workflows
- **[Random Model Replacement](features/random-model-replacement.md)** - Probabilistically replace models to improve session diversity and resilience
- **[Replacement Metrics](features/replacement-metrics.md)** - Track activation rates, turn counts, and opt-outs for replacements

### Response Processing

- **[Think Tags Fix](features/think-tags-fix.md)** - Correct improperly formatted thinking tags in model responses
- **[Auto Continue/Proceed Removal](features/auto-continue-removal.md)** - Strip mechanical "continue"/"proceed" messages from remote LLM submissions after interruptions
- **[Edit Precision Tuning](features/edit-precision.md)** - Automatically adjust temperature and top_p for code editing tasks

### Session Memory

- **[ProxyMem: Cross-Session Memory](proxymem-memory.md)** - Persistent context across sessions with LLM-generated summaries and intelligent context injection

### Development Tools

- **[Token Saving Guide](features/token-saving.md)** - Overview of both token-saving mechanisms and how to combine them
- **[Dynamic Tool Output Compression](features/dynamic-tool-output-compression.md)** - Strategy-based compression for tool outputs with content-aware reduction
- **[Pytest Context Saving](features/pytest-context-saving.md)** - Automatically add helpful pytest flags for better output
- **[Pytest Full-Suite Steering](features/pytest-full-suite-steering.md)** - Prevent agents from running entire test suites inadvertently
- **[Inline Python Steering](features/inline-python-steering.md)** - Control Python code execution within responses
- **[Test Execution Reminder](features/test-execution-reminder.md)** - Remind agents to run tests before completing tasks
- **[Session Management](features/session-management.md)** - Intelligent session handling and state management
- **[Context Compaction](features/context-compaction.md)** - Intelligent context compaction to reduce prompt size
- **[Context Window Enforcement](features/context-window-enforcement.md)** - Enforce context window limits and prevent overruns
- **[Windows Double-Ampersand Fixer](features/windows-double-ampersand-fixer.md)** - Automatically fix `&&` command separators for Windows clients
- **[Unified Steering Telemetry Migration](features/unified-steering-telemetry-migration.md)** - Migration guide for the unified steering framework telemetry changes

### Monitoring and Analytics

- **[Monitoring Overview](features/monitoring-overview.md)** - Overview of all monitoring and analytics capabilities
- **[Backend Health Checks](features/health-checks.md)** - Automated health monitoring and circuit breaker for backend API endpoints
- **[Connection Activity Monitoring](features/activity-monitoring.md)** - Real-time visibility into active connections with RX/TX byte counters
- **[Usage Tracking and Statistics](features/usage-tracking.md)** - Comprehensive monitoring of token consumption, costs, performance metrics, and request patterns across all backends

### Reliability and Resilience

- **[Failure Handling](features/failure-handling.md)** - Automatic retry and failover for backend errors
- **[Request Deduplication](features/request-deduplication.md)** - Prevent duplicate requests from exhausting rate limits
- **[Resilience Scoping](features/resilience-scoping.md)** - Personal vs shared cooldown state for OAuth and enterprise backends

### Client Integration

- **[WebSocket Transport for Responses API](features/websocket-transport.md)** - Low-latency WebSocket transport for `/v1/responses`
- **[Client Identity Override](features/identity-override.md)** - Override client identity headers for compatibility with specific tools

## Frontends

Frontend APIs where clients connect to the proxy:

- **[Frontend Overview](frontends/overview.md)** - Understanding frontends vs backends, choosing a frontend
- **[OpenAI Chat Completions](frontends/openai-chat-completions.md)** - `/v1/chat/completions` API for most OpenAI-compatible clients
- **[OpenAI Responses API](frontends/openai-responses.md)** - `/v1/responses` API for structured JSON output
- **[Anthropic Messages](frontends/anthropic.md)** - `/anthropic/v1/messages` API for Claude-compatible clients
- **[Google Gemini v1beta](frontends/gemini.md)** - `/v1beta/models` API for Gemini-compatible clients

## Backends

Backend provider configuration and usage:

- **[Backend Overview](backends/overview.md)** - Supported backends, choosing a backend, and switching between providers
- **[OpenAI Backend](backends/openai.md)** - OpenAI API and ChatGPT OAuth configuration
- **[OpenAI Codex Backend](backends/openai-codex.md)** - Codex CLI authentication and debugging-only usage
- **[Anthropic Backend](backends/anthropic.md)** - Claude API key configuration
- **[Alibaba Token Plan (International)](backends/alibaba-token-plan-intl.md)** - Token Plan models through Alibaba's Anthropic-compatible API
- **[Cline Backend](backends/cline.md)** - Internal development & debugging backend
- **[Gemini Backends](backends/gemini.md)** - Google Gemini API, OAuth, and GCP configurations
- **[Gemini OAuth Auto Backend](backends/gemini-oauth-auto.md)** - Multi-account Google Gemini with automatic rotation
- **[Antigravity OAuth Backend](backends/antigravity-oauth.md)** - Internal Antigravity OAuth configuration
- **[Kiro OAuth Auto Backend](backends/kiro-oauth-auto.md)** - Amazon Kiro / Q Developer streaming via self-managed OAuth

- **[Kimi Code Backend](backends/kimi-code.md)** - Kimi For Coding via OpenAI-compatible API
- **[OpenCode Go Backend](backends/opencode-go.md)** - OpenCode Go models behind one proxy backend with internal protocol routing
- **[CommandCode Backends](backends/commandcode.md)** - CommandCode OpenAI- and Anthropic-compatible gateway backends

- **[OpenRouter Backend](backends/openrouter.md)** - OpenRouter multi-model access
- **[Nvidia Backend](backends/nvidia.md)** - NVIDIA NIM OpenAI-compatible API
- **[ZAI Backend](backends/zai.md)** - Zhipu/Z.ai configuration
- **[Qwen Backend](backends/qwen.md)** - Alibaba Qwen OAuth configuration
- **[Minimax Backend](backends/minimax.md)** - Minimax API configuration
- **[InternLM Backend](backends/internlm.md)** - InternLM AI models with API key rotation
- **[Zenmux Backend](backends/zenmux.md)** - Zenmux API configuration
- **[Runinfra Backend](backends/runinfra.md)** - Runinfra (runinfra.ai) OpenAI-compatible API
- **[Ollama Backend](backends/ollama.md)** - Local model serving via Ollama
- **[OpenCode Zen Backend](backends/opencode-zen.md)** - OpenCode Zen API configuration
- **[WorkBuddy ACP Backend](backends/workbuddy-acp.md)** - WorkBuddy AI local CLI connector via native ACP over stdio
- **[Custom Backends](backends/custom-backends.md)** - Creating and configuring custom backend connectors

## Debugging

Tools and techniques for troubleshooting:

- **[Wire Capture](debugging/wire-capture.md)** - Record and analyze HTTP requests and responses
- **[CBOR Capture](debugging/cbor-capture.md)** - Binary wire capture format with simulation capabilities
- **[Troubleshooting Guide](debugging/troubleshooting.md)** - Common issues and solutions

## Security

Authentication and security best practices:

- **[Authentication](security/authentication.md)** - API key authentication and access control
- **[Brute-Force Protection](security/brute-force-protection.md)** - Rate limiting and attack prevention
- **[Key Hygiene](security/key-hygiene.md)** - API key redaction and secure handling

## Additional Resources

- **[Development Guide](../development_guide/index.md)** - For contributors and developers
- **[CHANGELOG](../../CHANGELOG.md)** - Version history and release notes
- **[CONTRIBUTING](../../CONTRIBUTING.md)** - How to contribute to the project
- **[LICENSE](../../LICENSE)** - Project license information

## Getting Help

If you encounter issues or have questions:

1. Check the [Troubleshooting Guide](debugging/troubleshooting.md)
2. Review the relevant feature or backend documentation
3. Search existing [GitHub Issues](https://github.com/matdev83/llm-interactive-proxy/issues)
4. Open a new issue with detailed information about your problem

## Quick Navigation

### By Use Case

- **First-time setup**: Start with [Quick Start Guide](quick-start.md)
- **Production deployment**: Review [Configuration Guide](configuration.md), [Database Configuration](database-configuration.md), and [Authentication](security/authentication.md)
- **Debugging issues**: See [Wire Capture](debugging/wire-capture.md) and [Troubleshooting](debugging/troubleshooting.md)
- **Advanced features**: Browse the [Features](#features) section
- **Backend setup**: Check [Backend Overview](backends/overview.md)

### By Role

- **End Users**: Quick Start, Configuration, Features, Backends
- **Security Administrators**: Security section, Tool Access Control, Authentication
- **Developers**: Development Guide, Debugging section, Wire Capture
- **DevOps**: Configuration, Authentication, Troubleshooting
