#!/usr/bin/env bash
# Scripted end-to-end smoke for a feature plugin against a stub backend.
#
# It builds aipstd and the scripted stub (tools/stubbackend) into the dev temp
# root, renders the proxy config template on per-run free ports, serves one
# request at the frontend path, and checks wire/backend/log assertions:
#
#   bash scripts/feature-stub-smoke.sh \
#     --config-template testdata/feature-stub-smoke/tool-name-correction/config.yaml.tmpl \
#     --responses testdata/feature-stub-smoke/tool-name-correction/responses \
#     --request testdata/feature-stub-smoke/tool-name-correction/request.json \
#     --frontend-path /v1/chat/completions \
#     --backend-requests 2 \
#     --assert-file testdata/feature-stub-smoke/tool-name-correction/assertions.txt
#
# Assertion arguments have the form <scope>:<pattern>:<count> with scope one of
# wire, backend or log; <pattern> is a fixed string matched per line, <count>
# the expected number of matching lines. --assert is repeatable; --assert-file
# holds one assertion per line (# comments and blanks ignored).
#
# Prints one line per assertion and a final PASS/FAIL, keeps every artifact
# under the dev temp root, always cleans up both processes with a trap, and
# exits non-zero on failure. POSIX-only: Windows coverage for these paths
# comes from the in-process dogfood tests (internal/stdhttp).
set -euo pipefail

CONFIG_TEMPLATE=""
RESPONSES=""
REQUEST=""
FRONTEND_PATH=""
BACKEND_REQUESTS=""
ASSERT_ARGS=()
ASSERT_FILES=()
TIMEOUT_SECS=60

usage() {
	cat >&2 <<'EOF'
Usage: scripts/feature-stub-smoke.sh --config-template PATH --responses PATH
       --request PATH --frontend-path PATH --backend-requests N
       [--assert SCOPE:PATTERN:COUNT ...] [--assert-file PATH ...]
       [--timeout SECS]
EOF
}

while (($# > 0)); do
	case "$1" in
		--config-template) CONFIG_TEMPLATE="${2:-}"; shift 2 ;;
		--responses) RESPONSES="${2:-}"; shift 2 ;;
		--request) REQUEST="${2:-}"; shift 2 ;;
		--frontend-path) FRONTEND_PATH="${2:-}"; shift 2 ;;
		--backend-requests) BACKEND_REQUESTS="${2:-}"; shift 2 ;;
		--assert) ASSERT_ARGS+=("${2:-}"); shift 2 ;;
		--assert-file) ASSERT_FILES+=("${2:-}"); shift 2 ;;
		--timeout) TIMEOUT_SECS="${2:-}"; shift 2 ;;
		-h | --help) usage; exit 0 ;;
		*) echo "feature-stub-smoke: unknown argument '$1'" >&2; usage; exit 2 ;;
	esac
done

[[ -n "$CONFIG_TEMPLATE" && -n "$RESPONSES" && -n "$REQUEST" && -n "$FRONTEND_PATH" && -n "$BACKEND_REQUESTS" ]] || {
	usage
	exit 2
}
[[ "$BACKEND_REQUESTS" =~ ^[0-9]+$ && "$TIMEOUT_SECS" =~ ^[0-9]+$ && "$TIMEOUT_SECS" -gt 0 ]] || {
	echo "feature-stub-smoke: --backend-requests and --timeout must be positive integers" >&2
	exit 2
}
for path in "$CONFIG_TEMPLATE" "$REQUEST"; do
	[[ -f "$path" ]] || {
		echo "feature-stub-smoke: file not found: $path" >&2
		exit 2
	}
done
[[ -e "$RESPONSES" ]] || {
	echo "feature-stub-smoke: responses path not found: $RESPONSES" >&2
	exit 2
}
for path in "${ASSERT_FILES[@]}"; do
	[[ -f "$path" ]] || {
		echo "feature-stub-smoke: assert file not found: $path" >&2
		exit 2
	}
done
command -v curl >/dev/null 2>&1 || {
	echo "feature-stub-smoke: curl is required" >&2
	exit 2
}

for path in "${ASSERT_FILES[@]}"; do
	while IFS= read -r line || [[ -n "$line" ]]; do
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line%"${line##*[![:space:]]}"}"
		[[ -z "$line" || "$line" == \#* ]] && continue
		ASSERT_ARGS+=("$line")
	done <"$path"
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SCRATCH_ROOT="${AIP_DEV_TMPDIR:-$HOME/.cache/aip-tmp}"
RUN_DIR="$(mktemp -d "${SCRATCH_ROOT}/feature-stub-smoke.XXXXXX")"
echo "artifacts: $RUN_DIR"

WIRE_LOG="$RUN_DIR/wire.sse"
STUB_LOG="$RUN_DIR/backend-requests.log"
PROXY_LOG="$RUN_DIR/proxy.log"
PROXY_CONFIG="$RUN_DIR/proxy.yaml"

echo "== build aipstd and stubbackend (GOWORK=off) =="
export GOWORK=off
go build -o "$RUN_DIR/aipstd" ./cmd/aipstd
go build -o "$RUN_DIR/stubbackend" ./tools/stubbackend

read -r PROXY_PORT STUB_PORT < <("$RUN_DIR/stubbackend" -print-free-ports 2)
echo "ports: proxy=127.0.0.1:$PROXY_PORT stub=127.0.0.1:$STUB_PORT"

sed -e "s|__PROXY_ADDR__|127.0.0.1:$PROXY_PORT|g" \
	-e "s|__BACKEND_BASE_URL__|http://127.0.0.1:$STUB_PORT/v1|g" \
	"$CONFIG_TEMPLATE" >"$PROXY_CONFIG"
if grep -q "__PROXY_ADDR__\|__BACKEND_BASE_URL__" "$PROXY_CONFIG"; then
	echo "feature-stub-smoke: config template tokens were not substituted" >&2
	exit 2
fi

"$RUN_DIR/stubbackend" -addr "127.0.0.1:$STUB_PORT" -responses "$RESPONSES" -log "$STUB_LOG" >"$RUN_DIR/stub-stdout.log" 2>&1 &
STUB_PID=$!
"$RUN_DIR/aipstd" --config "$PROXY_CONFIG" serve >"$PROXY_LOG" 2>&1 &
PROXY_PID=$!
cleanup() {
	kill "$STUB_PID" "$PROXY_PID" 2>/dev/null || true
	wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

wait_for_url() {
	local name="$1" url="$2" deadline=$((SECONDS + TIMEOUT_SECS))
	while ((SECONDS < deadline)); do
		if curl -s -o /dev/null --max-time 2 "$url" 2>/dev/null; then
			echo "ready: $name ($url)"
			return 0
		fi
		sleep 0.2
	done
	echo "feature-stub-smoke: FAIL waiting for $name ($url); logs in $RUN_DIR" >&2
	return 1
}

wait_for_url "stub backend" "http://127.0.0.1:$STUB_PORT/"
wait_for_url "proxy" "http://127.0.0.1:$PROXY_PORT/"

echo "== send request to $FRONTEND_PATH =="
HTTP_CODE="$(curl -s -S --max-time "$TIMEOUT_SECS" -X POST -H 'Content-Type: application/json' \
	--data @"$REQUEST" -o "$WIRE_LOG" -w '%{http_code}' \
	"http://127.0.0.1:$PROXY_PORT$FRONTEND_PATH")"
if [[ "$HTTP_CODE" != "200" ]]; then
	echo "ASSERT http-status:200 ... FAIL (got $HTTP_CODE; wire body:)" >&2
	cat "$WIRE_LOG" >&2 || true
	echo "FAIL feature-stub-smoke (non-200 response) artifacts=$RUN_DIR" >&2
	exit 1
fi
echo "ASSERT http-status:200 ... ok"

FAILURES=0
check_count() {
	local label="$1" file="$2" pattern="$3" want="$4"
	local found
	found="$(grep -F -c -- "$pattern" "$file" 2>/dev/null || true)"
	found="${found:-0}"
	if [[ "$found" == "$want" ]]; then
		echo "ASSERT $label ... ok (found $found)"
	else
		echo "ASSERT $label ... FAIL (found $found, want $want)"
		FAILURES=$((FAILURES + 1))
	fi
}

for arg in "${ASSERT_ARGS[@]}"; do
	scope="${arg%%:*}"
	rest="${arg#*:}"
	want="${rest##*:}"
	pattern="${rest%:*}"
	case "$scope" in
		wire) file="$WIRE_LOG" ;;
		backend) file="$STUB_LOG" ;;
		log) file="$PROXY_LOG" ;;
		*)
			echo "ASSERT $arg ... FAIL (unknown scope '$scope'; want wire, backend or log)"
			FAILURES=$((FAILURES + 1))
			continue
			;;
	esac
	if [[ -z "$pattern" || ! "$want" =~ ^[0-9]+$ ]]; then
		echo "ASSERT $arg ... FAIL (malformed assertion; want <scope>:<pattern>:<count>)"
		FAILURES=$((FAILURES + 1))
		continue
	fi
	check_count "$arg" "$file" "$pattern" "$want"
done

STUB_REQUESTS="$(grep -c '^REQ[0-9][0-9]* ' "$STUB_LOG" 2>/dev/null || true)"
STUB_REQUESTS="${STUB_REQUESTS:-0}"
if [[ "$STUB_REQUESTS" == "$BACKEND_REQUESTS" ]]; then
	echo "ASSERT backend-requests:$BACKEND_REQUESTS ... ok (found $STUB_REQUESTS)"
else
	echo "ASSERT backend-requests:$BACKEND_REQUESTS ... FAIL (found $STUB_REQUESTS, want $BACKEND_REQUESTS)"
	FAILURES=$((FAILURES + 1))
fi

if ((FAILURES == 0)); then
	echo "PASS feature-stub-smoke artifacts=$RUN_DIR"
	exit 0
fi
echo "FAIL feature-stub-smoke ($FAILURES failed assertions) artifacts=$RUN_DIR" >&2
exit 1
