# Requirements

## Goal and Boundary
Expose a bounded, provider-neutral HTTP workspace hint to existing local-agent consumers, enabling the authorized Claude ACP task 3.2 live session/workspace proof. HTTP ingress owns syntax and projection; backends retain filesystem validation, posture, credentials, policy and pool ownership. Core session/routing authority remains unchanged.

## Requirement 1: Explicit workspace ingress
1.1 When a stock HTTP inference request supplies `X-LIP-Workspace`, the frontend shall project its validated value to the existing canonical `workspace_path` extension through one shared parser, across OpenAI chat, OpenAI Responses, OpenResponses, Anthropic and Gemini ordinary and bounded-body inference paths.
1.2 When the header is absent or whitespace-only, decoding and workspace fallback shall remain unchanged.
1.3 When a nonempty value exceeds 4096 bytes, contains controls, is invalid UTF-8 or has relative/invalid absolute-path syntax, ingress shall reject it before execution with a static HTTP 400 diagnostic that does not echo the value.
1.4 The carrier shall follow existing explicit HTTP header alias/default resolution; workspace aliases colliding case-insensitively with other effective LIP header aliases shall reject configuration. Arbitrary body extension passthrough shall remain unsupported.

## Requirement 2: Authority and isolation
2.1 The projection shall remain an untrusted request hint, never a routing/session authorization decision, filesystem authorization grant or instruction to change the host's cwd. Existing local-agent access posture and backend filesystem validation shall remain authoritative.
2.2 The hint shall add only `workspace_path`, without overwriting existing canonical workspace hints, session identifiers, route decisions, provider/authentication/MCP policy or a proxy-authoritative workspace context view.
2.3 A stable client session with different workspace hints shall reach existing backend workspace-key isolation. Compatible repeats and returns shall follow existing pool reuse/replacement rules without new pool state.

## Requirement 3: Delivery proof
3.1 Shared syntax/alias/projection and existing frontend-family seams shall have red-first bounded regressions, including absent and invalid input, without a frontend-by-backend test matrix.
3.2 A spawned stock proxy and packaged local-agent consumer shall demonstrate that the supplied session workspace, not `default_workspace`, becomes the owned process cwd, with two-project isolation and compatible return observations. The approved Claude task 3.2 live test supplies this proof; final acceptance remains open until that run succeeds.

## Deferred
No body workspace carrier, arbitrary extension passthrough, persistent workspace registry, new canonical field/ABI, host chdir, platform certification expansion, routing change, billing change or multi-user policy change.
