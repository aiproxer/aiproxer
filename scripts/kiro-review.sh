#!/usr/bin/env bash
# kiro-review.sh runs the kiro-review workflow as a best-of-n panel: several
# headless OpenCode agents from different model families review the same
# scope concurrently, then a stronger judge performs its own full review with
# their reports as unverified suggestions.
#
#   scripts/kiro-review.sh <spec> "<scope>" [--context FILE] [--repo DIR] [--out DIR] [--timeout SECONDS]
#
#   <spec>     directory name under .kiro/specs/ (e.g. tool-result-elision)
#   <scope>    what to review, as you would pass it to /kiro-review: task IDs,
#              a range, or a short description
#   --context  file with controller context every agent receives verbatim: exact
#              task text, implementer artifact path, validation commands
#   --repo     checkout to review (default: the git toplevel of the current directory)
#   --out      telemetry directory holding run output (default: $KIRO_REVIEW_OUT,
#              else .local/kiro-review-panel-telemetry in the repository container,
#              else ${XDG_STATE_HOME:-~/.local/state}/kiro-review-panel-telemetry);
#              must be outside the repo
#   --timeout  per-agent wall-clock limit in seconds (default: $KIRO_REVIEW_TIMEOUT or 3600)
#
# The roster: which model runs as r1, as r2 and as judge. Each entry is in the
# provider/model#variant form `opencode run -m` accepts; the variant is the
# reasoning effort. The roster comes from the roster file, and from nowhere
# else: KIRO_REVIEW_CONFIG names the file, else the container's .env (the
# container layout keeps one file outside every checkout, shared by all of
# them), else ~/.config/kiro-review/models.env. The environment is not
# consulted, so a stale variable in some shell cannot silently change who
# reviews. The file holds KIRO_REVIEW_R1, KIRO_REVIEW_R2, KIRO_REVIEW_JUDGE,
# and optionally KIRO_REVIEW_REVIEWERS to replace the reviewer set wholesale
# for a panel of any size. The built-in defaults below are the last resort.
# The roster file is host-local and lives outside the repository, so swapping
# a model never needs a pull request. KIRO_REVIEW_HEARTBEAT sets the
# progress-summary interval in seconds (default 30), and
# KIRO_REVIEW_OPENCODE_LOG_LEVEL the level of each agent's private OpenCode
# server log, <run>/<id>/opencode.log (default info).
#
# On completion stdout carries KEY=value lines for a calling agent: RUN_DIR,
# VERDICT, REPORT (judge Markdown) and RESULT (judge handoff JSON, when written).
#
# Every agent event is narrated to stderr and to <run>/run.log, prefixed with a
# timestamp and the agent id. Each run keeps its transcripts: per agent the
# prompt, raw events (<run>/<id>/events.jsonl), report and OpenCode server log.
# Every run, including failed and interrupted ones, ends by writing
# <run>/summary.json (timings, statuses, verdicts, tokens, tool use per agent)
# and appending the same object as one line to <telemetry>/runs.jsonl.
#
# Exit codes:
#   0 judge verdict APPROVED
#   1 the run failed (judge failed, or produced no verdict)
#   2 usage error
#   3 judge verdict REJECTED
set -euo pipefail

# ---- configuration: one value per role --------------------------------------
# The built-in defaults are the last resort: the roster file and the
# environment decide what actually runs (see resolve_roster).
DEFAULT_R1="opencode/deepseek-v4.1-flash-free#max"
DEFAULT_R2="opencode/beam-free#max"
DEFAULT_JUDGE="openai/gpt-6.1-sol#medium"
# ------------------------------------------------------------------------------

OPENCODE_BIN="${OPENCODE_BIN:-opencode}"
HEARTBEAT="${KIRO_REVIEW_HEARTBEAT:-30}"
OPENCODE_LOG_LEVEL="${KIRO_REVIEW_OPENCODE_LOG_LEVEL:-info}"
TIMEOUT="${KIRO_REVIEW_TIMEOUT:-3600}"
OUT_PARENT="${KIRO_REVIEW_OUT:-}"
REPO=""
CONTEXT_FILE=""
# Reports larger than this are truncated in the judge prompt; the judge is
# pointed at the full file instead.
readonly INLINE_REPORT_LIMIT=60000

usage() {
	sed -n '2,44p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

die() {
	echo "kiro-review: $*" >&2
	exit 2
}

positional=()
while (($#)); do
	case "$1" in
	-h | --help) usage; exit 0 ;;
	--context) CONTEXT_FILE="${2:?--context needs a value}"; shift 2 ;;
	--repo) REPO="${2:?--repo needs a value}"; shift 2 ;;
	--out) OUT_PARENT="${2:?--out needs a value}"; shift 2 ;;
	--timeout) TIMEOUT="${2:?--timeout needs a value}"; shift 2 ;;
	-*) die "unknown flag: $1" ;;
	*) positional+=("$1"); shift ;;
	esac
done
((${#positional[@]} == 2)) || { usage >&2; exit 2; }
SPEC="${positional[0]}"
SCOPE="${positional[1]}"

# Panel agents are marked so a stray recursive launch is refused, not nested.
[[ -z "${KIRO_REVIEW_PANEL:-}" ]] ||
	die "already inside a kiro-review panel (as ${KIRO_REVIEW_PANEL}); perform the review yourself"
[[ -z "$CONTEXT_FILE" || -r "$CONTEXT_FILE" ]] || die "--context file not readable: $CONTEXT_FILE"
[[ "$TIMEOUT" =~ ^[0-9]+$ ]] || die "--timeout must be whole seconds: $TIMEOUT"
[[ "$HEARTBEAT" =~ ^[0-9]+$ && "$HEARTBEAT" -gt 0 ]] || die "KIRO_REVIEW_HEARTBEAT must be positive seconds"
for tool in jq timeout git "$OPENCODE_BIN"; do
	command -v "$tool" >/dev/null || die "required tool not found: $tool"
done

REPO="$(git -C "${REPO:-.}" rev-parse --show-toplevel 2>/dev/null)" || die "not a git checkout: ${REPO:-.}"
SPEC_DIR="$REPO/.kiro/specs/$SPEC"
[[ -d "$SPEC_DIR" ]] || die "spec not found: $SPEC_DIR"
[[ -f "$REPO/.agents/skills/kiro-review/SKILL.md" ]] || die "kiro-review skill missing in $REPO"

# container_root prints the container directory that holds the checkouts, when
# the repository uses the container layout (<container>/branches/<name>/.git is
# the shared git directory). A plain clone has no container root and prints
# nothing.
container_root() {
	local common
	common="$(git -C "$REPO" rev-parse --path-format=absolute --git-common-dir)"
	[[ "$(basename "$(dirname "$(dirname "$common")")")" == branches ]] || return 0
	dirname "$(dirname "$(dirname "$common")")"
}

# default_out locates the telemetry directory beside the checkouts when the
# repository uses the container layout, so every worktree's runs land in one
# place.
default_out() {
	local container
	container="$(container_root)"
	if [[ -n "$container" ]]; then
		echo "$container/.local/kiro-review-panel-telemetry"
	else
		echo "${XDG_STATE_HOME:-$HOME/.local/state}/kiro-review-panel-telemetry"
	fi
}

# roster_file prints the file the roster is read from, or nothing when there
# is none: KIRO_REVIEW_CONFIG names the file, else the container's .env (the
# container layout keeps one file outside every checkout, shared by all of
# them), else the per-user path. It names a file; it never carries a model.
# A file inside the repository is never used: it would change the reviewed
# source.
roster_file() {
	if [[ -n "${KIRO_REVIEW_CONFIG:-}" ]]; then
		echo "$KIRO_REVIEW_CONFIG"
		return 0
	fi
	local container fallback
	container="$(container_root)"
	if [[ -n "$container" && -f "$container/.env" ]]; then
		echo "$container/.env"
		return 0
	fi
	fallback="${XDG_CONFIG_HOME:-$HOME/.config}/kiro-review/models.env"
	if [[ -f "$fallback" ]]; then
		echo "$fallback"
	fi
	return 0
}

# load_roster_file reads the roster file into ROSTER_FILE_VALUES. Only known
# keys are read, values are never evaluated as shell, and a '#' starts a
# comment only at the start of a line or after whitespace, so the '#max'
# reasoning suffix in a model id survives.
load_roster_file() {
	local file=$1 line key value
	[[ -n "$file" && -r "$file" ]] || return 0
	while IFS= read -r line || [[ -n "$line" ]]; do
		line="${line#"${line%%[![:space:]]*}"}"
		if [[ "$line" == \#* ]]; then
			continue
		fi
		line="${line%%[[:space:]]#*}"
		if [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*(.*)$ ]]; then
			key="${BASH_REMATCH[1]}"
			value="${BASH_REMATCH[2]}"
		else
			continue
		fi
		case "$key" in
		KIRO_REVIEW_R1 | KIRO_REVIEW_R2 | KIRO_REVIEW_JUDGE | KIRO_REVIEW_REVIEWERS) ;;
		*) continue ;;
		esac
		value="${value%"${value##*[![:space:]]}"}"
		if [[ -n "$value" ]]; then
			ROSTER_FILE_VALUES["$key"]="$value"
		fi
	done <"$file"
}

# roster_pick <key> <default> sets ROSTER_VALUE to the value one role runs
# with, and records in ROSTER_ORIGINS where that value came from. The roster
# file is the only source: the environment is not consulted, so no stale
# variable can change who reviews.
roster_pick() {
	local key=$1 fallback=$2
	if [[ -n "${ROSTER_FILE_VALUES[$key]:-}" ]]; then
		ROSTER_ORIGINS["$key"]="file:$ROSTER_FILE"
		ROSTER_VALUE="${ROSTER_FILE_VALUES[$key]}"
	else
		ROSTER_ORIGINS["$key"]="default"
		ROSTER_VALUE="$fallback"
	fi
}

# resolve_roster decides which models run. The roster file decides; the
# built-in defaults only cover a checkout that has no roster file. Every
# value's origin is recorded, so a run never silently uses a roster other
# than the intended one.
resolve_roster() {
	declare -g -A ROSTER_FILE_VALUES=() ROSTER_ORIGINS=()
	ROSTER_VALUE=""
	ROSTER_FILE="$(roster_file)"
	load_roster_file "$ROSTER_FILE"

	local list="" r1 r2 judge
	if [[ -n "${ROSTER_FILE_VALUES[KIRO_REVIEW_REVIEWERS]:-}" ]]; then
		list="${ROSTER_FILE_VALUES[KIRO_REVIEW_REVIEWERS]}"
		ROSTER_ORIGINS[reviewers]="file:$ROSTER_FILE"
	fi

	if [[ -n "$list" ]]; then
		read -r -a REVIEWERS <<<"$list"
		roster_pick KIRO_REVIEW_JUDGE "$DEFAULT_JUDGE"
		judge="$ROSTER_VALUE"
		ROSTER_SOURCE="reviewers=${ROSTER_ORIGINS[reviewers]} judge=${ROSTER_ORIGINS[KIRO_REVIEW_JUDGE]}"
	else
		roster_pick KIRO_REVIEW_R1 "$DEFAULT_R1"
		r1="$ROSTER_VALUE"
		roster_pick KIRO_REVIEW_R2 "$DEFAULT_R2"
		r2="$ROSTER_VALUE"
		roster_pick KIRO_REVIEW_JUDGE "$DEFAULT_JUDGE"
		judge="$ROSTER_VALUE"
		REVIEWERS=("$r1" "$r2")
		ROSTER_SOURCE="r1=${ROSTER_ORIGINS[KIRO_REVIEW_R1]} r2=${ROSTER_ORIGINS[KIRO_REVIEW_R2]} judge=${ROSTER_ORIGINS[KIRO_REVIEW_JUDGE]}"
	fi
	JUDGE="$judge"
}

resolve_roster
((${#REVIEWERS[@]} > 0)) || die "no reviewers configured"

OUT_PARENT="${OUT_PARENT:-$(default_out)}"
mkdir -p "$OUT_PARENT"
OUT_PARENT="$(cd "$OUT_PARENT" && pwd -P)"
case "$OUT_PARENT/" in
"$(cd "$REPO" && pwd -P)/"*) die "--out must be outside the repository (writing there changes the reviewed source)" ;;
esac
RUN_DIR="$OUT_PARENT/$(date +%Y%m%d-%H%M%S)-${SPEC}-$$"
mkdir -p "$RUN_DIR"
RUN_LOG="$RUN_DIR/run.log"
RUN_STARTED="$(date +%s)"
FP_START="" FP_MID="" FP_END="" PHASE1_S="" PHASE2_S="" verdict=""

log() {
	printf '%s [panel] %s\n' "$(date +%T)" "$*" | tee -a "$RUN_LOG" >&2
}

# fingerprint identifies the reviewed source: HEAD, the working-tree diff and
# untracked file bytes. Reviewers are told to stay read-only; this proves it.
fingerprint() {
	{
		git -C "$REPO" rev-parse HEAD
		git -C "$REPO" diff HEAD --binary
		(cd "$REPO" && git ls-files --others --exclude-standard -z | xargs -0 -r sha256sum --)
	} | sha256sum | cut -c1-16
}

# narrate turns OpenCode JSON events into one readable log line each.
# shellcheck disable=SC2016 # jq program, not shell
readonly NARRATE_JQ='
def cut(n): if length > n then .[:n] + "…" else . end;
def flat: gsub("\\s+"; " ");
. as $line | (try fromjson catch null) as $e
| if $e == null then "raw: " + ($line | cut(300))
  elif $e.type == "step_start" then empty
  elif $e.type == "tool_use" then
    "tool " + ($e.part.tool // "?") + " [" + ($e.part.state.status // "?") + "] "
    + (($e.part.state.input // {}) | tojson | cut(240))
    + (if $e.part.state.status == "error" then " ERROR: " + (($e.part.state.error // "") | tostring | flat | cut(300)) else "" end)
  elif $e.type == "text" then "text: " + ($e.part.text // "" | flat | cut(400))
  elif $e.type == "reasoning" then "think: " + ($e.part.text // "" | flat | cut(240))
  elif $e.type == "step_finish" then
    "step done (" + ($e.part.reason // "?") + ") tokens in=\($e.part.tokens.input // 0) out=\($e.part.tokens.output // 0)"
    + " reasoning=\($e.part.tokens.reasoning // 0) cache_read=\($e.part.tokens.cache.read // 0) cost=\($e.part.cost // 0)"
  elif $e.type == "error" then "ERROR: " + ($e.error // $e | tojson | cut(600))
  else ($e.type // "event") + ": " + ($e | tojson | cut(240))
  end'

narrate() {
	local id=$1 line
	jq --unbuffered -R -r "$NARRATE_JQ" | while IFS= read -r line; do
		printf '%s [%s] %s\n' "$(date +%T)" "$id" "$line" | tee -a "$RUN_LOG" >&2
	done
}

# Agents run with --standalone, so each session lives in a private server that
# dies with its process. `timeout` leads its own process group, out of reach of
# the Ctrl-C that kills this script's subshells, so an early exit signals each
# still-running agent's group directly; run_agent records its id in agent.pgid.
# shellcheck disable=SC2317 # invoked by the EXIT trap
on_exit() {
	local status=$? pgid_file
	trap - EXIT INT TERM
	for pgid_file in "$RUN_DIR"/*/agent.pgid; do
		[[ -f "$pgid_file" && ! -f "${pgid_file%/*}/exit_code" ]] || continue
		kill -TERM -- "-$(cat "$pgid_file")" 2>/dev/null || true
	done
	write_summary "$status" || log "telemetry: failed to write summary.json"
	exit "$status"
}

# agent_summary <id> <role> <model> prints one agent's metrics as compact JSON.
# shellcheck disable=SC2317 # reached through on_exit
agent_summary() {
	local id=$1 role=$2 model=$3 dir="$RUN_DIR/$1" report="" source=none events=/dev/null
	if [[ -s "$dir/report.md" ]]; then
		report="$dir/report.md" source=report.md
	elif [[ -s "$dir/final-text.md" ]]; then
		report="$dir/final-text.md" source=final-text
	fi
	[[ -f "$dir/events.jsonl" ]] && events="$dir/events.jsonl"
	jq -R -s -c \
		--arg id "$id" --arg role "$role" --arg model "$model" \
		--arg exit "$(cat "$dir/exit_code" 2>/dev/null)" \
		--arg started "$(cat "$dir/started_at" 2>/dev/null)" \
		--arg finished "$(cat "$dir/finished_at" 2>/dev/null)" \
		--arg source "$source" --arg verdict "$([[ -n "$report" ]] && verdict_of "$report")" \
		--argjson report_bytes "$([[ -n "$report" ]] && wc -c <"$report" || echo 0)" '
		def num: tonumber? // null;
		[split("\n")[] | select(length > 0) | try fromjson catch empty] as $e
		| ($e | map(select(.type == "tool_use"))) as $tools
		| ($e | map(select(.type == "step_finish") | .part)) as $steps
		| def total(f): $steps | map(f // 0) | add // 0;
		{id: $id, role: $role, model: $model,
		 exit_code: ($exit | num), timed_out: ($exit == "124"),
		 started_at: ($started | num), finished_at: ($finished | num),
		 duration_s: (if ($started | num) and ($finished | num) then ($finished | num) - ($started | num) else null end),
		 session_id: ([$e[].sessionID // empty] | first),
		 delivered: ($verdict != ""), report_source: $source, report_bytes: $report_bytes,
		 verdict: (if $verdict == "" then null else $verdict end),
		 steps: ($steps | length),
		 tokens: {input: total(.tokens.input), output: total(.tokens.output),
		          reasoning: total(.tokens.reasoning), cache_read: total(.tokens.cache.read)},
		 cost: total(.cost),
		 tool_calls: ($tools | length),
		 tool_errors: ($tools | map(select(.part.state.status == "error")) | length),
		 tools_by_name: ($tools | group_by(.part.tool) | map({key: (.[0].part.tool // "?"), value: length}) | from_entries),
		 errors: ($e | map(select(.type == "error") | (.error // .) | tostring | .[:300]))}' "$events"
}

# write_summary <exit-status> records the run in <run>/summary.json and appends
# it to <telemetry>/runs.jsonl, the cross-run index for later analysis.
# shellcheck disable=SC2317 # reached through on_exit
write_summary() {
	local status=$1 i agents="" result="$RUN_DIR/judge/result.json"
	for i in "${!REVIEWERS[@]}"; do
		[[ -d "$RUN_DIR/r$((i + 1))" ]] && agents+="$(agent_summary "r$((i + 1))" reviewer "${REVIEWERS[$i]}")"$'\n'
	done
	[[ -d "$RUN_DIR/judge" ]] && agents+="$(agent_summary judge judge "$JUDGE")"$'\n'
	jq -s -c \
		--arg run_id "$(basename "$RUN_DIR")" --arg spec "$SPEC" --arg scope "$SCOPE" \
		--argjson started "$RUN_STARTED" --argjson finished "$(date +%s)" --argjson exit "$status" \
		--arg head "$(git -C "$REPO" rev-parse HEAD)" --arg branch "$(git -C "$REPO" branch --show-current)" \
		--arg opencode "$("$OPENCODE_BIN" --version 2>/dev/null)" --arg judge_model "$JUDGE" \
		--argjson timeout "$TIMEOUT" --argjson context "$([[ -n "$CONTEXT_FILE" ]] && echo true || echo false)" \
		--arg fp_start "$FP_START" --arg fp_mid "$FP_MID" --arg fp_end "$FP_END" \
		--arg phase1 "$PHASE1_S" --arg phase2 "$PHASE2_S" --arg verdict "$verdict" \
		--arg roster_source "$ROSTER_SOURCE" --arg roster_file "$ROSTER_FILE" \
		--slurpfile result <(if [[ -s "$result" ]] && jq -e . "$result" >/dev/null 2>&1; then cat "$result"; fi) '
		def num: tonumber? // null;
		def opt: if . == "" then null else . end;
		. as $agents
		| ($agents | map(select(.role == "reviewer"))) as $rev
		| ($agents | map(select(.role == "judge")) | first) as $judge
		| {schema: 1, run_id: $run_id, spec: $spec, scope: $scope,
		   started_at: ($started | todate), finished_at: ($finished | todate), duration_s: ($finished - $started),
		   exit_code: $exit,
		   outcome: ({"0": "approved", "3": "rejected", "130": "interrupted", "143": "terminated"}[$exit | tostring] // "failed"),
		   verdict: ($verdict | opt),
		   source: {head: $head, branch: ($branch | opt), context_file: $context,
		            fingerprint: {start: ($fp_start | opt), after_reviewers: ($fp_mid | opt), after_judge: ($fp_end | opt)},
		            drift: (($fp_mid != "" and $fp_mid != $fp_start) or ($fp_end != "" and $fp_end != $fp_mid))},
		   config: {reviewers: ($rev | map(.model)), judge: $judge_model, timeout_s: $timeout, opencode: ($opencode | opt),
		            roster_source: $roster_source, roster_file: ($roster_file | opt)},
		   phases_s: {reviewers: ($phase1 | num), judge: ($phase2 | num)},
		   reviewers_total: ($rev | length),
		   reviewers_delivered: ($rev | map(select(.delivered)) | length),
		   reviewer_verdicts: ($rev | map({(.id): .verdict}) | add),
		   judge_agrees_with: ($rev | map(select(.verdict != null and .verdict == $judge.verdict) | .id)),
		   judge_result: (if ($result | length) > 0
		     then {present: true, status: $result[0].status,
		           findings_by_severity: ($result[0].findings // [] | group_by(.severity) | map({key: (.[0].severity // "?"), value: length}) | from_entries)}
		     else {present: false} end),
		   tokens_total: ($agents | map(.tokens) | {input: (map(.input) | add // 0), output: (map(.output) | add // 0), reasoning: (map(.reasoning) | add // 0)}),
		   cost_total: ($agents | map(.cost) | add // 0),
		   agents: $agents}' <<<"$agents" >"$RUN_DIR/summary.json"
	jq -c . "$RUN_DIR/summary.json" >>"$OUT_PARENT/runs.jsonl"
	log "telemetry: $RUN_DIR/summary.json (indexed in $OUT_PARENT/runs.jsonl)"
}
trap on_exit EXIT
trap 'log "interrupted; stopping agents"; exit 130' INT
trap 'log "terminated; stopping agents"; exit 143' TERM

# run_agent <id> <model> runs one headless OpenCode session in the repository
# with the prompt at <run>/<id>/prompt.md and records its exit status.
run_agent() {
	local id=$1 model=$2 dir="$RUN_DIR/$1" status
	printf '%s [%s] start model=%s\n' "$(date +%T)" "$id" "$model" | tee -a "$RUN_LOG" >&2
	date +%s >"$dir/started_at"
	set +e
	# --standalone: a private server per agent, so the session ends with the
	# process instead of outliving a timeout inside the shared background service.
	(cd "$REPO" && export KIRO_REVIEW_PANEL="$id" && echo "$BASHPID" >"$dir/agent.pgid" && exec timeout --kill-after=30 "$TIMEOUT" "$OPENCODE_BIN" run --standalone --auto \
		--format json --thinking --print-logs --log-level "$OPENCODE_LOG_LEVEL" \
		--title "kiro-review $SPEC [$id]" -m "$model" <"$dir/prompt.md" 2>"$dir/opencode.log") |
		tee "$dir/events.jsonl" | narrate "$id"
	status=${PIPESTATUS[0]}
	set -e
	date +%s >"$dir/finished_at"
	echo "$status" >"$dir/exit_code"
	if ((status != 0)) && [[ -s "$dir/opencode.log" ]]; then
		sed "s/^/$(date +%T) [$id] opencode.log: /" "$dir/opencode.log" | tail -n 20 | tee -a "$RUN_LOG" >&2
	fi
	printf '%s [%s] exit=%s%s\n' "$(date +%T)" "$id" "$status" "$([[ $status == 124 ]] && echo ' (timed out)')" |
		tee -a "$RUN_LOG" >&2
}

# agent_stats prints a one-line progress summary from an agent's event file.
agent_stats() {
	local events=$1
	[[ -s "$events" ]] || { echo "no events yet"; return; }
	jq -R -s -r '
		[split("\n")[] | select(length > 0) | try fromjson catch empty] as $e
		| ($e | map(select(.type == "tool_use"))) as $tools
		| ($e | map(select(.type == "step_finish") | .part.tokens)) as $tok
		| "events=\($e | length) steps=\($tok | length) tools=\($tools | length)"
		  + " tokens_in=\($tok | map(.input // 0) | add // 0) tokens_out=\($tok | map(.output // 0) | add // 0)"
		  + " reasoning=\($tok | map(.reasoning // 0) | add // 0)"
		  + " last_tool=\($tools | last | .part.tool // "-")"' "$events"
}

# wait_agents <id:pid>... blocks until every agent exits, printing a heartbeat.
wait_agents() {
	local started=$SECONDS beat=$SECONDS entry alive
	while :; do
		alive=0
		for entry in "$@"; do
			kill -0 "${entry#*:}" 2>/dev/null && alive=1
		done
		((alive)) || break
		# Poll every second so phase timings stay exact; log only every heartbeat.
		sleep 1 &
		wait $! || true
		((SECONDS - beat >= HEARTBEAT)) || continue
		beat=$SECONDS
		for entry in "$@"; do
			local id=${entry%%:*} state=finished
			kill -0 "${entry#*:}" 2>/dev/null && state=running
			log "heartbeat ${id} ${state} elapsed=$((SECONDS - started))s $(agent_stats "$RUN_DIR/$id/events.jsonl")"
		done
	done
	for entry in "$@"; do
		wait "${entry#*:}" || true
	done
}

# collect_report <id> prints the path of the agent's report, falling back to
# its final assistant text when it did not write the file.
collect_report() {
	local id=$1 dir="$RUN_DIR/$1"
	if [[ ! -s "$dir/report.md" && -s "$dir/events.jsonl" ]]; then
		jq -R -s -r '[split("\n")[] | select(length > 0) | try fromjson catch empty
			| select(.type == "text") | .part.text] | join("\n\n")' "$dir/events.jsonl" >"$dir/final-text.md"
		if [[ -s "$dir/final-text.md" ]]; then
			log "$id did not write report.md; using its final text as the report"
			echo "$dir/final-text.md"
			return
		fi
	fi
	[[ -s "$dir/report.md" ]] && echo "$dir/report.md"
	return 0
}

verdict_of() {
	grep -oE '^VERDICT: (APPROVED|REJECTED)' "$1" 2>/dev/null | tail -n 1 | cut -d' ' -f2 || true
}

context_block() {
	[[ -n "$CONTEXT_FILE" ]] || return 0
	printf 'Controller context (from the caller; verify it, do not trust it):\n<controller-context>\n'
	cat "$CONTEXT_FILE"
	printf '\n</controller-context>\n'
}

shared_brief() {
	cat <<EOF
Run the kiro-review workflow on this repository, equivalent to \`/kiro-review $SPEC $SCOPE\`:
- Read $REPO/.agents/skills/kiro-review/SKILL.md first and follow it.
- Spec: $SPEC_DIR (requirements.md, design.md, tasks.md, spec.json).
- Review scope: $SCOPE
  Resolve the exact task text, its _Boundary:_, and the referenced requirement and design
  sections from tasks.md yourself. Inspect the actual changes (git diff against the merge base
  with origin/main, plus any uncommitted changes) rather than trusting any summary.

$(context_block)
You run headless: nobody will answer questions. When something is ambiguous, choose the most
reasonable interpretation, state it in your report, and continue.
EOF
}

reviewer_prompt() {
	local id=$1 dir="$RUN_DIR/$1"
	mkdir -p "$dir/scratch"
	cat <<EOF
You are reviewer $id on a best-of-n code review panel. Other reviewers are working in this
same checkout at the same time.

$(shared_brief)

Constraints:
- The repository is read-only for you. Do not create, edit, delete, format or stage files inside
  $REPO. Do not commit, push, stash, reset, or switch branches. Do not run generators or anything
  that rewrites tracked files.
- Read-only checks (go test, go vet, linters) are fine. Prefer package-scoped commands over
  full-repository gates to stay inside the time limit of ${TIMEOUT}s.
- Put scratch output and logs under $dir/scratch.
- Do not write the reviewer JSON handoff artifact; the panel judge owns it.

Deliverable: write your complete review as Markdown to $dir/report.md with:
1. Scope as you understood it, and any assumptions.
2. Mechanical checks you actually ran: command, exit code, one-line result.
3. Findings: severity (Critical/Important/Suggestion/FYI), file:line, evidence, spec reference,
   required remediation.
4. A one-sentence summary.
The last line must be exactly \`VERDICT: APPROVED\` or \`VERDICT: REJECTED\`.
If you cannot write that file, output the complete report as your final message instead.
EOF
}

judge_prompt() {
	local dir="$RUN_DIR/judge" entry id model report status size
	mkdir -p "$dir/scratch"
	cat <<EOF
You are the final judge of a best-of-n code review panel.

$(shared_brief)

Perform this review yourself, independently and in full.

Other reviewers, running models from different families, reviewed the same scope concurrently.
Their reports are below. Treat them as unverified suggestions, never as ground truth:
- Verify every finding independently against the code, the spec and command output you run
  yourself before adopting it. Reject findings you cannot confirm and say why.
- Their findings are not the boundary of your review. Do your own complete review; issues none of
  them raised are expected and are the point of your independent pass.
- Agreement between reviewers is not evidence; they may share a mistake. A reviewer's verdict
  carries no weight on its own.
EOF
	if [[ -n "$DRIFT_NOTE" ]]; then
		printf '\nWARNING: %s\n' "$DRIFT_NOTE"
	fi
	for entry in "${PANEL[@]}"; do
		IFS='|' read -r id model status report <<<"$entry"
		printf '\n<reviewer-report id="%s" model="%s" exit="%s"' "$id" "$model" "$status"
		if [[ -z "$report" ]]; then
			printf ' missing="true">\nThis reviewer produced no report.\n</reviewer-report>\n'
			continue
		fi
		size=$(wc -c <"$report")
		printf ' path="%s">\n' "$report"
		head -c "$INLINE_REPORT_LIMIT" "$report"
		if ((size > INLINE_REPORT_LIMIT)); then
			printf '\n[truncated at %s of %s bytes; read the full report at %s]\n' "$INLINE_REPORT_LIMIT" "$size" "$report"
		fi
		printf '\n</reviewer-report>\n'
	done
	cat <<EOF

Deliverables:
1. The reviewer JSON artifact described in $REPO/docs/agent-handoffs.md, written to
   $dir/result.json (role reviewer, task "$SPEC: $SCOPE"), with evidence logs under
   $dir/scratch, validated as that document describes.
2. Your full review as Markdown at $dir/report.md: scope and assumptions, mechanical checks you
   ran (command, exit code, result), findings (severity, file:line, evidence, spec reference,
   remediation), and a "Panel reconciliation" section that lists each reviewer finding as
   confirmed, rejected (with the reason) or unverifiable, followed by findings you raised
   independently. End with a one-sentence summary. The last line must be exactly
   \`VERDICT: APPROVED\` or \`VERDICT: REJECTED\`.

The repository is read-only for you as well: do not modify, stage or commit anything inside $REPO.
If you cannot write report.md, output the complete report as your final message instead.
EOF
}

# ---- run ---------------------------------------------------------------------
log "run dir: $RUN_DIR"
log "repo: $REPO (HEAD $(git -C "$REPO" rev-parse --short HEAD), branch $(git -C "$REPO" branch --show-current))"
log "spec: $SPEC  scope: $SCOPE"
log "reviewers: ${REVIEWERS[*]}"
log "judge: $JUDGE  timeout/agent: ${TIMEOUT}s  heartbeat: ${HEARTBEAT}s"
log "roster source: $ROSTER_SOURCE"
log "opencode: $("$OPENCODE_BIN" --version 2>/dev/null || echo unknown)"
FP_START="$(fingerprint)"
log "source fingerprint: $FP_START"

phase_start=$SECONDS
pids=()
for i in "${!REVIEWERS[@]}"; do
	id="r$((i + 1))"
	mkdir -p "$RUN_DIR/$id"
	reviewer_prompt "$id" >"$RUN_DIR/$id/prompt.md"
	run_agent "$id" "${REVIEWERS[$i]}" &
	pids+=("$id:$!")
done
log "phase 1: ${#pids[@]} reviewers running concurrently"
wait_agents "${pids[@]}"
PHASE1_S=$((SECONDS - phase_start))
log "phase 1 finished in ${PHASE1_S}s"

PANEL=()
for i in "${!REVIEWERS[@]}"; do
	id="r$((i + 1))"
	status="$(cat "$RUN_DIR/$id/exit_code" 2>/dev/null || echo '?')"
	report="$(collect_report "$id")"
	PANEL+=("$id|${REVIEWERS[$i]}|$status|$report")
	if [[ -n "$report" ]]; then
		log "$id (${REVIEWERS[$i]}): exit=$status verdict=$(verdict_of "$report" || true) report=$report ($(wc -l <"$report") lines)"
	else
		log "$id (${REVIEWERS[$i]}): exit=$status, no report"
	fi
done

DRIFT_NOTE=""
FP_MID="$(fingerprint)"
if [[ "$FP_MID" != "$FP_START" ]]; then
	DRIFT_NOTE="the repository changed while the reviewers ran (fingerprint $FP_START -> $FP_MID). A reviewer may have modified files; inspect git status and treat findings that depend on those changes with suspicion."
	log "WARNING: $DRIFT_NOTE"
	git -C "$REPO" status --short | sed 's/^/    /' | tee -a "$RUN_LOG" >&2
fi

phase_start=$SECONDS
mkdir -p "$RUN_DIR/judge"
judge_prompt >"$RUN_DIR/judge/prompt.md"
log "phase 2: judge prompt $(wc -c <"$RUN_DIR/judge/prompt.md") bytes"
run_agent judge "$JUDGE" &
wait_agents "judge:$!"
PHASE2_S=$((SECONDS - phase_start))
log "phase 2 finished in ${PHASE2_S}s"

FP_END="$(fingerprint)"
[[ "$FP_END" == "$FP_MID" ]] || log "WARNING: the repository changed while the judge ran ($FP_MID -> $FP_END)"

judge_report="$(collect_report judge)"
verdict=""
[[ -n "$judge_report" ]] && verdict="$(verdict_of "$judge_report")"
log "judge: exit=$(cat "$RUN_DIR/judge/exit_code") verdict=${verdict:-none} report=${judge_report:-none}"
[[ -s "$RUN_DIR/judge/result.json" ]] && log "judge artifact: $RUN_DIR/judge/result.json"
log "full log: $RUN_LOG"
printf 'RUN_DIR=%s\nVERDICT=%s\nREPORT=%s\n' "$RUN_DIR" "${verdict:-NONE}" "$judge_report"
[[ -s "$RUN_DIR/judge/result.json" ]] && printf 'RESULT=%s\n' "$RUN_DIR/judge/result.json"

case "$verdict" in
APPROVED) exit 0 ;;
REJECTED) exit 3 ;;
*) log "no verdict from the judge"; exit 1 ;;
esac
