# SecretGuard token-edit experiment (#817)

## Recommendation

Do not replace the production rewrite matcher with this prototype. Explicit
token edits can remove traversal counters, but this experiment does not establish
a smaller *equivalent* enforcement pipeline. No production code is changed.

## Consumer and concept ledger

| Existing concept | Consumer / invariant | Can the prototype retire it? |
| --- | --- | --- |
| BetterLeaks literal occurrences | `betterLeaksLiteralCandidates`, original-byte/field/span proof | No; edit proposals still need trusted occurrence identity |
| Exact positional occurrences | `collectExactPrivateFindingsForRedact`, safe hybrid attribution | No; public Matcher also permits non-positional implementations |
| Canonical JSON token walker | duplicate-key last-wins, key/scalar classification, first-value semantics | No; physical token spans alone admit discarded shadows |
| Lazy string mapping | Unicode/invalid UTF-8 raw-to-decoded range proof | No; escaping and partial credentials still need it |
| `jsonIndex` / `jsonCandidateIndex` | stateful matcher invocation order | Potentially; explicit token membership replaces temporal coupling |
| Prefix-aware overlapping masks | `applyBetterLeaksRanges`, exact-first union | No; token replacement proposals must preserve this policy |
| Coverage | `recordCoverage`, `validateCoverage`, fail closed before publication | No; proposals require complete original-occurrence coverage too |
| Working clone | `guard.evalRedact`, atomic commit after successful scan | No; a validated edit engine is not call-level atomic publication |
| Safe findings | `mergeHybridFindings`, SDK decision validation | No; edit spans do not supply attribution/cardinality metadata |

## Bounded prototype

`token_edit_prototype_test.go` is branch-local research, not a new SDK API or
production rewriting framework. It validates non-overlapping whole-string token
proposals, original-byte identity, effective canonical token membership,
cancellation and valid JSON output. It preserves untouched raw bytes.

The membership test rejects both object keys and discarded duplicate-key values.
It exercises escaped strings and rejects mismatched original-byte proofs. The
prototype deliberately does not implement detector discovery, safe findings,
multipart coverage, exact/BetterLeaks overlap, prefix preservation, or malformed
JSON opaque fallback.

`BenchmarkTokenEditPrototype` compares validation/application mechanisms for
precomputed proposals at 1 KiB and 2 MiB, with no-hit and positive controls. It is
not an end-to-end Guard benchmark: proposal construction and safe finding policy
are absent on the edit side. Its results cannot certify replacement of the bridge.

## Where equivalence stops

- Physical editing without canonical membership can turn a previously blocked
  secret in a discarded duplicate-key shadow into an accepted request.
- Returning raw token edits preserves ordering, duplicate entries and escapes
  inside changed JSON; the current changed-value path remarshals canonical JSON.
  This may be a useful future behavior change, but it is not a behavior-preserving
  replacement and needs an explicit contract decision.
- The prototype accepts complete valid JSON only. Existing private traversal
  supports the first JSON value plus trailing input, and the guard has an opaque
  fallback on decode failure. Removing those paths would weaken compatibility.
- Existing Matchers can change output length or report findings without a byte
  replacement. Literal edit eligibility must remain independent of findings,
  and incomplete BetterLeaks coverage must still block atomic publication.
- Multipart primary/component attribution and prefix-aware overlap are not
  recoverable from a bare list of token replacements.

## Mechanism measurements and proof

Six samples per case, `-cpu=2 -benchtime=100ms -benchmem`, measured the same
compact single-string JSON fixture and validated the expected masked output.
Whole-token edit proposals, including their replacement bytes, were constructed
outside the timed loop. The bridge rebuilds its request-local traversal state.

| Input | Bridge median B/op | Edits median B/op | Bridge median ms/op | Edits median ms/op |
| --- | ---: | ---: | ---: | ---: |
| 1 KiB no-hit | 7,640 | 1,024 | 0.0251 | 0.0209 |
| 1 KiB positive | 12,133 | 2,296 | 0.0602 | 0.0396 |
| 2 MiB no-hit | 18,872,808 | 2,097,456 | 45.9 | 53.0 |
| 2 MiB positive | 29,361,064 | 4,194,896 | 81.3 | 71.8 |

The lower allocations indicate an interesting validation/application mechanism,
not equivalent enforcement. In particular, the edit side excludes replacement
proposal construction, exact matcher calls, and safe-finding production. It
cannot be compared with the complete Guard workloads from #818. Shared-host
timing is provisional; even the reduced mechanism was slower on 2 MiB no-hit
input, so there is no universal CPU improvement claim.

The prototype membership/original-byte tests passed. Removing the object-key
exclusion made its negative control fail; the exclusion was restored. Raw
measurements are retained in `secretguard-token-edit-prototype.txt` in task scratch.

## Implementation verdict

Production concepts eliminated: **zero**. Two counters are potential removals,
but the prototype adds an edit proposal model, validation/application boundary,
token-membership proof and coverage bookkeeping. Counting its subset implementation
against the complete bridge would falsely report a large simplification.

The narrow eager-tree and dedup changes (#815/#816) deliver measurable reductions
without this semantic change. JSON ownership work belongs to #818. A production
token-edit replacement remains NO-GO until an equivalent full prototype materially
reduces concepts and passes the issue's differential, cancellation, allocation and
remote race gates. No 64 MiB or request-latency certification is claimed.
