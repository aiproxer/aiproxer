# AIProxer

**Any agent. Any model. Your subscriptions. One proxy.**

AIProxer sits between your AI agents and the models they use. Your favourite coding agent can talk to any provider, your existing subscriptions work wherever you work, and long agent sessions keep running when something upstream goes wrong.

> **Alpha / pre-release.** AIProxer is in active development and has not reached a stable release. Expect rough edges, breaking configuration changes between versions, and features that are still maturing. Try it, push it, and tell us what breaks, but don't build anything critical on it yet.

---

## The problems we solve

**Your agent decides your model.** Most AI agents are built around one vendor's API. Want to use Claude Code with a different model, or Codex with a model it doesn't ship with? Normally you can't.
→ AIProxer translates between AI APIs on the fly, so any supported agent can use any supported model.

**You pay for subscriptions you can't use where you work.** You already pay for ChatGPT, Claude, Grok, or other plans, but each one is locked to its vendor's own tools.
→ AIProxer lets you bring the subscriptions and API keys you already have to the agent of your choice. Your subscriptions stay yours: AIProxer never shares or resells a personal plan's allowance to anyone else.

**Long agent sessions die for avoidable reasons.** A rate limit, an exhausted key, a malformed tool call, or a model that quits halfway through the task, and an hour of agent work is gone.
→ AIProxer fails over between providers, repairs broken tool calls, and keeps agents on task, without interrupting your session.

**You can't see or control what your agents do.** Usage is spread across vendors, secrets slip into prompts, and nobody can say what was spent where.
→ AIProxer gives you one place to observe, protect, and control all AI traffic.

---

## What you get in the Alpha

### Connect anything to anything
- **Works with the agents you already use.** AIProxer speaks the OpenAI (Responses and Chat Completions), Anthropic Messages, and Google Gemini APIs. Agents such as Claude Code, OpenAI Codex, Gemini CLI, OpenCode, and any tool that lets you set a custom API endpoint just point at AIProxer.
- **140+ providers out of the box.** OpenAI, Anthropic, Google Gemini, xAI, Meta, DeepSeek, Mistral, Groq, Together, Moonshot/Kimi, MiniMax, Qwen, and many more, ready to use with only an API key.
- **Enterprise clouds.** Azure OpenAI, Google Vertex AI, Amazon Bedrock and SageMaker, Oracle OCI, IBM watsonx, SAP AI Core, Snowflake Cortex, Databricks, and Cloudflare.
- **Model hubs and local models.** OpenRouter, Hugging Face, NVIDIA, Cohere, and Replicate, plus Ollama, llama.cpp, LM Studio, and vLLM for models on your own hardware.
- **Mix models freely.** Use models with completely different APIs in the same session; AIProxer handles the translation.

### Bring your own subscriptions and keys
- **Subscription bridges.** Use your ChatGPT plan (through OpenAI Codex), xAI/Grok, Qwen, MiniMax, Nous Portal, and GitLab Duo accounts with other agents.
- **Your Claude subscription, your way.** Use your own Claude plan through the official Claude Code runtime, for your personal use.

For the experimental, CLI-only native-login/external-subagent split, see
[Claude Code hybrid routing](docs/claude-code-hybrid.md) (live acceptance unverified).
- **Cursor** through its official SDK is experimental and delivered as the standalone [`aiproxer-cursor-sdk`](https://github.com/aiproxer/aiproxer-cursor-sdk) backend plugin. The published plugin release works with host `v0.1.0`; current builds need plugin 0.2.0, which ships with the next host release ([#1404](https://github.com/aiproxer/aiproxer-enterprise/issues/1404)).
- **Multiple keys per provider.** Register several API keys for one provider. Automatic quota-aware rotation, which moves to the next key before your agent sees an error, is landing during the Alpha.

### Keep long sessions alive
- **Automatic failover.** If a provider fails before it starts answering, AIProxer retries on the next provider in your route.
- **Tool-call repair.** Malformed or truncated tool calls are repaired in flight, and misnamed tools are corrected, so your agent doesn't crash on a model's mistake.
- **Agent loop guard.** Stops models from ending an agent's task before the work is actually done.
- **Stream recovery.** Interrupted responses can resume instead of starting over.

### Route smarter
- **Your routing, your rules.** Ordered failover, weighted load-sharing, and parallel "fastest wins" races, with time-to-first-token budgets.
- **Override hard-coded model names.** Map the model names your tools insist on to whatever model you actually want to use.
- **Sticky sessions** keep a conversation on the same backend when that matters.
- **Planner/executor pairing.** Let one model think and plan while another executes.

### Steer models
- **Model-specific instructions.** Fix known quirks of particular models or model families with targeted steering, without touching your agent's prompts.
- **Pre-request policies.** Check requests with a helper model before they reach the main one.

### Save tokens and money
- **Prompt-cache keep-warm** and cache-friendly routing, to cut the cost of cache misses on long sessions.
- **Cache-health warnings** when something in a session starts defeating the provider's cache.

### Stay safe
- **Secret-leak prevention.** AIProxer can detect your API keys and other configured secrets in outgoing prompts and redact or block them before they reach a model provider.
- **Secure by default.** Local-only by default, explicit authentication for shared deployments, and network access controls including GeoIP filtering.

### See everything
- **Usage metering** across every provider in one place.
- **Prometheus metrics and OpenTelemetry tracing** for your existing monitoring.
- **Live configuration reload** without restarting or dropping connections.

---

## On the roadmap

Coming to AIProxer, the free and open-source core:

- **Cross-agent memory.** Agent memory that follows you across sessions, agents, and devices, instead of being locked inside one tool.
- **Intelligent steering.** Bring distracted models back on task and catch obvious mistakes before they waste time and tokens.
- **Dangerous tool-call blocking.** Automatic detection and blocking of destructive agent actions.
- **Token compression.** Fit more work into every context window and every plan.
- **Usage-window warm-up.** Choose when vendor usage windows (such as 5-hour session limits) start, so they match how you actually work.
- **Transcript and usage reporting.** Cross-vendor transcripts and usage reports, including subscription plans and multi-account setups.
- **In-session control.** Change how the proxy behaves from inside your agent session, as you work.

---

## AIProxer Enterprise

**Vendor-independent control of AI usage across your whole organisation.** AIProxer Enterprise is a commercial add-on, coming soon, that builds on the open-source core.

**Govern access**
- A web dashboard for managers.
- Automated provisioning of AI access through SSO, SAML, RADIUS, and other identity systems, so nobody emails API keys around.
- Control which teams and people can use which models.
- Budgets and usage limits per organisational unit, department, team, and user.
- Live and historical usage reporting, with charts, breakdowns, filters, and aggregations.
- Central, searchable storage of session transcripts.

**Cut costs**
- **Pareto routing.** Pick the best trade-off of speed, cost, and quality for each request, according to your preferences.
- **In-session slicing.** Route different parts of one session to different models for the best cost/quality balance.
- **Least-cost routing.** When several providers can serve a request, use the cheapest first, with no change to any client.

**Agent Shield: security for AI agents**
- GDPR / PII redaction.
- An independent "judge" that checks sessions against your organisation's AI-usage policy.
- Protection for public-facing agents against prompt injection, context poisoning, and users trying to push agents into misconduct.
- A tool-call firewall with allow lists and deny lists.
- Modality enforcement, which blocks injection attempts hidden in images, voice, or other non-text input.

Interested in early access? [Open an issue](https://github.com/aiproxer/aiproxer/issues) and tell us about your use case.

---

## Get started

1. **Download** the archive for your platform (Linux, macOS, or Windows; x64 or ARM64) from [GitHub Releases](https://github.com/aiproxer/aiproxer/releases), and verify it against the published `checksums.txt`.
2. **Configure** your providers in a YAML file, starting from the sample [`config/config.yaml`](config/config.yaml). API keys are read from environment variables, never from the config file.
3. **Check and start** the proxy:

   ```bash
   aipstd check-config --config config.yaml
   aipstd serve --config config.yaml
   ```

4. **Point your agent** at `http://127.0.0.1:8080`, for example with `OPENAI_BASE_URL=http://127.0.0.1:8080/v1` or `ANTHROPIC_BASE_URL=http://127.0.0.1:8080`.

Optional connectors (enterprise clouds, subscription bridges, local runtimes) are installed separately; see the [connector guide](docs/backend-plugins/operator.md). More configuration examples are in [`config/examples/`](config/examples/), and the full documentation is in [`docs/`](docs/).

---

## Open core, MIT licensed

AIProxer is free and open-source software under the MIT licence. It is complete and useful on its own for individuals and single operators, and it will never be deliberately limited to sell an upgrade. AIProxer Enterprise adds what organisations need on top: governance, cost control, and compliance.

Found a bug or missing a provider or agent? [Open an issue](https://github.com/aiproxer/aiproxer/issues). Alpha feedback directly shapes what we build next.
