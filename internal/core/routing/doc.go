// Package routing parses route selector strings and expands them into ordered attempt candidates.
//
// Grammar (v1): model-only, backend:model, backend.instance:model, failover (a|b|c),
// weighted ([weight=N]a^[weight=M]b), [first] on weighted branches, parallel (a!b!c)
// with per-leg [handicap=N] and [ttft_timeout=N], and ?query parameters.
// Parallel and weighted operators cannot be mixed in the same arm; failover (|) of
// parallel groups is allowed (a!b|c!d).
// The planner applies exclusions, optional health filtering, session first-request rules,
// and deterministic weighted selection via an injected RNG.
package routing
