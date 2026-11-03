#!/usr/bin/env bash
# Proves kiro-review.sh runs its reviewers concurrently with the configured
# models, hands their reports to the judge, and exits on the judge's verdict.
# A fake opencode stands in for the agents: the script's contract is the
# orchestration, not what a model says.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

# The roster comes from a file and from nowhere else, so the harness points
# the script at its own roster file and points the per-user fallback at an
# empty directory: the machine's real roster must not reach the test.
export XDG_CONFIG_HOME="$fixture/xdg"
mkdir -p "$XDG_CONFIG_HOME"
cat >"$fixture/roster.env" <<'ENV'
KIRO_REVIEW_R1=prov/alpha#max
KIRO_REVIEW_R2=prov/beta#max
KIRO_REVIEW_JUDGE=prov/judge#medium
ENV

repo="$fixture/repo"
git init --quiet --initial-branch=feat "$repo"
mkdir -p "$repo/.kiro/specs/demo" "$repo/.agents/skills/kiro-review"
echo '- [ ] 1.1 demo task' >"$repo/.kiro/specs/demo/tasks.md"
echo '# kiro-review' >"$repo/.agents/skills/kiro-review/SKILL.md"
git -C "$repo" add -A
git -C "$repo" commit --quiet -m seed

# The fake records its argv and stdin, refuses to proceed for a reviewer until
# every reviewer has started (a sequential run times out here), then writes a
# report to the path named in its prompt.
bin="$fixture/bin"
mkdir -p "$bin"
cat >"$bin/opencode" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "--version" ]] && { echo fake-1.0; exit 0; }
args="$*"
title="" model=""
while (($#)); do
	case "$1" in
	--title) title=$2; shift 2 ;;
	-m) model=$2; shift 2 ;;
	*) shift ;;
	esac
done
id=${title##*[}; id=${id%]}
prompt=$(cat)
printf '%s\n' "$prompt" >"$FAKE_STATE/prompt-$id"
echo "$model" >"$FAKE_STATE/model-$id"
echo "$args" >"$FAKE_STATE/args-$id"
echo "${KIRO_REVIEW_PANEL:-}" >"$FAKE_STATE/panel-$id"
if [[ "$id" == r* ]]; then
	touch "$FAKE_STATE/started-$id"
	for _ in $(seq 50); do
		(($(ls "$FAKE_STATE" | grep -c '^started-') >= 2)) && break
		sleep 0.1
	done
	(($(ls "$FAKE_STATE" | grep -c '^started-') >= 2)) || { echo "reviewers ran sequentially" >&2; exit 9; }
fi
echo "{\"type\":\"step_start\",\"sessionID\":\"ses_$id\"}"
[[ "$id" == "${FAKE_HANG:-}" ]] && exec -a "fake-hang-$FAKE_STATE" sleep 60
echo '{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"git diff"}}}}'
echo '{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":0}},"cost":0}}'
# The deliverable path comes last; the judge prompt also quotes reviewer paths.
report=$(grep -oE '/[^ "]*/report\.md' <<<"$prompt" | tail -n 1)
verdict=APPROVED
[[ "$id" == judge ]] && verdict=$FAKE_JUDGE_VERDICT
printf 'finding from %s\nVERDICT: %s\n' "$id" "$verdict" >"$report"
echo '{"type":"text","part":{"text":"done"}}'
EOF
chmod +x "$bin/opencode"

fails=0
fail() {
	echo "FAIL: $*" >&2
	fails=$((fails + 1))
}

# Exercise the parser without launching a panel, including grep failures that
# must not become an empty result or a partially emitted approval.
# shellcheck source=/dev/null
source <(sed -n '/^verdict_of() {/,/^}/p' "$script_dir/kiro-review.sh")
check_verdict() {
	local file=$1 want_status=$2 want_text=$3 text status
	if text=$(verdict_of "$file" 2>"$fixture/verdict.err"); then
		status=0
	else
		status=$?
	fi
	[[ "$status" == "$want_status" && "$text" == "$want_text" ]] ||
		fail "verdict parser: exit=$status text=$text, want exit=$want_status text=$want_text"
}
printf 'VERDICT: APPROVED\nVERDICT: REJECTED\n' >"$fixture/verdict.md"
check_verdict "$fixture/verdict.md" 0 REJECTED
echo 'review incomplete' >"$fixture/verdict.md"
check_verdict "$fixture/verdict.md" 1 ''
check_verdict "$fixture/missing.md" 2 ''
[[ -s "$fixture/verdict.err" ]] || fail 'verdict parser: read failure diagnostic was hidden'
if ! (
	# Called indirectly by verdict_of, whose definition is loaded above.
	# shellcheck disable=SC2317
	grep() { printf 'VERDICT: APPROVED\n'; return 2; }
	if text=$(verdict_of "$fixture/verdict.md"); then status=0; else status=$?; fi
	[[ "$status" == 2 && -z "$text" ]]
); then
	fail 'verdict parser: accepted partial output from a failed grep'
fi
((fails)) || echo 'ok: verdict parser distinguishes match, no match and check failure'

# run_case <judge-verdict> <expected-exit> [extra args...]
run_case() {
	local verdict=$1 want=$2 state="$fixture/state-$1" status
	shift 2
	mkdir -p "$state"
	echo "implementer artifact: /abs/impl-$verdict.json" >"$state/context.txt"
	FAKE_STATE="$state" FAKE_JUDGE_VERDICT="$verdict" PATH="$bin:$PATH" \
		KIRO_REVIEW_CONFIG="$fixture/roster.env" \
		KIRO_REVIEW_HEARTBEAT=1 KIRO_REVIEW_OUT="$fixture/out-$verdict" \
		bash "$script_dir/kiro-review.sh" --repo "$repo" --context "$state/context.txt" "$@" demo "1.1" >"$state/stdout" 2>"$state/stderr" &&
		status=0 || status=$?
	[[ "$status" == "$want" ]] || { fail "$verdict: exit $status, want $want"; cat "$state/stderr" >&2; return; }
	[[ "$(cat "$state/model-r1")" == "prov/alpha#max" ]] || fail "$verdict: r1 model"
	[[ "$(cat "$state/model-r2")" == "prov/beta#max" ]] || fail "$verdict: r2 model"
	[[ "$(cat "$state/model-judge")" == "prov/judge#medium" ]] || fail "$verdict: judge model"
	grep -q -- '--standalone' "$state/args-r1" || fail "$verdict: agents not run on a private server"
	[[ "$(cat "$state/panel-r2")" == r2 ]] || fail "$verdict: agents not marked as panel members"
	grep -q 'finding from r1' "$state/prompt-judge" || fail "$verdict: judge prompt lacks r1 report"
	grep -q 'finding from r2' "$state/prompt-judge" || fail "$verdict: judge prompt lacks r2 report"
	grep -q 'unverified suggestions' "$state/prompt-judge" || fail "$verdict: judge not told reports are suggestions"
	grep -q '/kiro-review demo 1.1' "$state/prompt-r1" || fail "$verdict: reviewer prompt lacks the workflow"
	grep -q "impl-$verdict.json" "$state/prompt-r1" || fail "$verdict: reviewer prompt lacks --context"
	grep -q "impl-$verdict.json" "$state/prompt-judge" || fail "$verdict: judge prompt lacks --context"
	grep -qx "VERDICT=$verdict" "$state/stdout" || fail "$verdict: stdout lacks VERDICT=$verdict"
	report=$(sed -n 's/^REPORT=//p' "$state/stdout")
	[[ "$report" == */judge/report.md && -s "$report" ]] || fail "$verdict: stdout REPORT does not name the judge report"
	grep -q '\[r1\] tool shell \[completed\]' "$state/stderr" || fail "$verdict: tool events not narrated"
	grep -q 'heartbeat\|phase 1 finished' "$state/stderr" || fail "$verdict: no progress logging"
	[[ -z "$(git -C "$repo" status --porcelain)" ]] || fail "$verdict: run wrote into the repository"
	summary=$(sed -n 's/^RUN_DIR=//p' "$state/stdout")/summary.json
	jq -e --arg v "$verdict" '.verdict == $v and .reviewers_delivered == 2 and (.judge_agrees_with == ["r1", "r2"]) == ($v == "APPROVED")
		and (.agents | length) == 3 and all(.agents[]; .duration_s != null and .tool_calls == 1)' "$summary" >/dev/null ||
		fail "$verdict: summary.json missing or wrong"
	[[ "$(wc -l <"$fixture/out-$verdict/runs.jsonl")" == 1 ]] || fail "$verdict: run not indexed in runs.jsonl"
	((fails)) || echo "ok: judge $verdict -> exit $want"
}

run_case APPROVED 0
run_case REJECTED 3

# hang_env <state-dir> runs kiro-review with reviewer r2 hanging.
hang_run() {
	local state=$1
	shift
	mkdir -p "$state"
	FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED FAKE_HANG=r2 PATH="$bin:$PATH" \
		KIRO_REVIEW_CONFIG="$fixture/roster.env" \
		KIRO_REVIEW_HEARTBEAT=1 KIRO_REVIEW_OUT="$state/out" \
		bash "$script_dir/kiro-review.sh" --repo "$repo" "$@" demo "1.1" >/dev/null 2>"$state/stderr"
}

# A timed-out reviewer is killed, and the judge still runs on the other report.
state="$fixture/state-timeout"
hang_run "$state" --timeout 2 && status=0 || status=$?
if [[ "$status" != 0 ]]; then
	fail "timeout: exit $status, want 0"
	cat "$state/stderr" >&2
elif ! grep -q 'r2.*exit=124 (timed out)' "$state/stderr"; then
	fail "timeout: not logged"
elif pgrep -f "fake-hang-$state" >/dev/null; then
	fail "timeout: hung agent survived"
elif ! jq -e '.reviewers_delivered == 1 and (.agents[] | select(.id == "r2") | .timed_out and (.delivered | not))' \
	"$state"/out/*/summary.json >/dev/null; then
	fail "timeout: summary does not record the timed-out reviewer"
else
	echo "ok: timed-out reviewer is killed; judge still runs"
fi

# Stopping the script stops its agents: timeout moves them out of the
# script's process group, so nothing else would reach them.
state="$fixture/state-term"
hang_run "$state" &
runner=$!
for _ in $(seq 50); do
	pgrep -f "fake-hang-$state" >/dev/null && break
	sleep 0.1
done
# Like Ctrl-C, this also hits the script's subshells, not only its main process.
pkill -TERM -f "kiro-review.sh --repo $repo demo" || true
wait "$runner" && status=0 || status=$?
sleep 0.5
if [[ "$status" != 143 ]]; then
	fail "term: exit $status, want 143"
elif pgrep -f "fake-hang-$state" >/dev/null; then
	pkill -f "fake-hang-$state" || true
	fail "term: agent survived the script"
elif ! jq -e '.outcome == "terminated"' "$state"/out/*/summary.json >/dev/null; then
	fail "term: interrupted run left no summary"
else
	echo "ok: terminating the script kills its agents"
fi

# A panel agent that launches the panel again is refused instead of nesting.
if KIRO_REVIEW_PANEL=r1 PATH="$bin:$PATH" bash "$script_dir/kiro-review.sh" --repo "$repo" demo x 2>"$fixture/nested.err"; then
	fail "nested launch inside a panel was accepted"
elif ! grep -q 'already inside a kiro-review panel' "$fixture/nested.err"; then
	fail "nested launch refused without saying why"
else
	echo "ok: refuses a nested launch from inside a panel"
fi

# In the container layout, runs from every checkout land in one telemetry
# directory beside the checkouts.
container="$fixture/container"
mkdir -p "$container/branches"
git clone --quiet "$repo" "$container/branches/main"
state="$fixture/state-container"
mkdir -p "$state"
FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED PATH="$bin:$PATH" KIRO_REVIEW_HEARTBEAT=1 \
	bash "$script_dir/kiro-review.sh" --repo "$container/branches/main" demo "1.1" >/dev/null 2>&1 || true
if [[ -s "$container/.local/kiro-review-panel-telemetry/runs.jsonl" ]]; then
	echo "ok: container layout records runs beside the checkouts"
else
	fail "container layout: no runs.jsonl in <container>/.local/kiro-review-panel-telemetry"
fi

# The roster file lives beside the checkouts, outside every one of them, so
# swapping a model never touches the repository.
cat >"$container/.env" <<'ENV'
# A comment line, and a key the panel does not own.
IGNORED=not-a-roster-key
KIRO_REVIEW_R1=roster/one#max
KIRO_REVIEW_R2=roster/two#max   # a trailing comment
KIRO_REVIEW_JUDGE=roster/judge#medium
ENV
state="$fixture/state-roster"
mkdir -p "$state"
FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED PATH="$bin:$PATH" KIRO_REVIEW_HEARTBEAT=1 \
	KIRO_REVIEW_OUT="$state/out" \
	bash "$script_dir/kiro-review.sh" --repo "$container/branches/main" demo "1.1" >"$state/stdout" 2>"$state/stderr" || true
r1="$(cat "$state/model-r1" 2>/dev/null)"
r2="$(cat "$state/model-r2" 2>/dev/null)"
judge="$(cat "$state/model-judge" 2>/dev/null)"
if [[ "$r1" == "roster/one#max" && "$r2" == "roster/two#max" && "$judge" == "roster/judge#medium" ]]; then
	echo "ok: reads the roster from the container .env"
else
	fail "roster file: r1=$r1 r2=$r2 judge=$judge"
fi
summary=$(sed -n 's/^RUN_DIR=//p' "$state/stdout")/summary.json
if jq -e --arg f "$container/.env" '.config.roster_file == $f and (.config.roster_source | test("file"))' "$summary" >/dev/null; then
	echo "ok: the run records where its roster came from"
else
	fail "roster file: summary.json does not record the roster source"
fi

# The environment does not decide the roster: the file does. A stale variable
# in some shell must not change who reviews.
state="$fixture/state-roster-env"
mkdir -p "$state"
FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED PATH="$bin:$PATH" KIRO_REVIEW_HEARTBEAT=1 \
	KIRO_REVIEW_OUT="$state/out" KIRO_REVIEW_R1="env/one#max" KIRO_REVIEW_JUDGE="env/judge#medium" \
	KIRO_REVIEW_REVIEWERS="env/one#max env/two#max" \
	KIRO_REVIEW_CONFIG="$fixture/roster.env" \
	bash "$script_dir/kiro-review.sh" --repo "$container/branches/main" demo "1.1" >"$state/stdout" 2>"$state/stderr" || true
if [[ "$(cat "$state/model-r1" 2>/dev/null)" == "prov/alpha#max" &&
	"$(cat "$state/model-r2" 2>/dev/null)" == "prov/beta#max" &&
	"$(cat "$state/model-judge" 2>/dev/null)" == "prov/judge#medium" ]]; then
	echo "ok: the environment does not override the roster file"
else
	fail "roster file: the environment won over the file (r1=$(cat "$state/model-r1" 2>/dev/null))"
fi

# KIRO_REVIEW_CONFIG names the roster file explicitly.
state="$fixture/state-roster-config"
mkdir -p "$state"
printf 'KIRO_REVIEW_R1=config/one#max\nKIRO_REVIEW_R2=config/two#max\nKIRO_REVIEW_JUDGE=config/judge#medium\n' >"$fixture/roster-explicit.env"
FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED PATH="$bin:$PATH" KIRO_REVIEW_HEARTBEAT=1 \
	KIRO_REVIEW_OUT="$state/out" KIRO_REVIEW_CONFIG="$fixture/roster-explicit.env" \
	bash "$script_dir/kiro-review.sh" --repo "$container/branches/main" demo "1.1" >"$state/stdout" 2>"$state/stderr" || true
if [[ "$(cat "$state/model-r1" 2>/dev/null)" == "config/one#max" &&
	"$(cat "$state/model-judge" 2>/dev/null)" == "config/judge#medium" ]]; then
	echo "ok: KIRO_REVIEW_CONFIG names the roster file"
else
	fail "roster file: KIRO_REVIEW_CONFIG was not used"
fi

# The roster file can replace the reviewer set wholesale, for a panel of any
# size.
state="$fixture/state-roster-list"
mkdir -p "$state"
printf 'KIRO_REVIEW_REVIEWERS=list/one#max list/two#max list/three#max\nKIRO_REVIEW_JUDGE=list/judge#medium\n' >"$fixture/roster-list.env"
FAKE_STATE="$state" FAKE_JUDGE_VERDICT=APPROVED PATH="$bin:$PATH" KIRO_REVIEW_HEARTBEAT=1 \
	KIRO_REVIEW_OUT="$state/out" KIRO_REVIEW_CONFIG="$fixture/roster-list.env" \
	bash "$script_dir/kiro-review.sh" --repo "$container/branches/main" demo "1.1" >"$state/stdout" 2>"$state/stderr" || true
if [[ "$(cat "$state/model-r1" 2>/dev/null)" == "list/one#max" &&
	"$(cat "$state/model-r3" 2>/dev/null)" == "list/three#max" &&
	"$(cat "$state/model-judge" 2>/dev/null)" == "list/judge#medium" ]]; then
	echo "ok: the roster file can replace the reviewer set"
else
	fail "roster file: KIRO_REVIEW_REVIEWERS in the file was not used"
fi

# Run output inside the reviewed repository would change the source under review.
if KIRO_REVIEW_OUT="$repo/out" PATH="$bin:$PATH" bash "$script_dir/kiro-review.sh" --repo "$repo" demo x \
	>/dev/null 2>&1; then
	fail "accepted --out inside the repository"
else
	echo "ok: refuses output inside the repository"
fi
rm -rf "$repo/out"

((fails == 0)) || { echo "$fails failure(s)" >&2; exit 1; }
echo "all kiro-review checks passed"
