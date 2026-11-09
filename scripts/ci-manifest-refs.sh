#!/usr/bin/env bash
# Print the commit refs whose trees the repository file manifest check must
# verify, one per line, oldest first. The caller always verifies HEAD
# separately afterwards, so this lists only the additional history refs.
#
# Inputs from the environment (mirroring the CI manifest step):
#   EVENT_NAME  github event name (schedule, workflow_dispatch, push, ...)
#   BEFORE_SHA  github.event.before for push events (empty or all-zero when
#               there is no pushed range)
#   PUSHED_REF  github.ref of the pushed branch (for new-branch exclusion)
#
# Schedule and manual runs list nothing: they validate the checked-out tree
# itself. History is immutable, so scanning every historical commit can never
# go green again once a past commit drifts (for example a manifest entry
# whose file arrived in a later commit); only the HEAD tree can be released.
set -euo pipefail

event_name="${EVENT_NAME:-}"
before_sha="${BEFORE_SHA:-}"
pushed_ref="${PUSHED_REF:-}"
zero_sha="0000000000000000000000000000000000000000"

if [[ "$event_name" == "pull_request" || "$event_name" == "merge_group" ]]; then
	# The final PR tree is authoritative; the manifest may be completed
	# by a later commit than the files it approves.
	exit 0
fi

if [[ "$event_name" == "schedule" || "$event_name" == "workflow_dispatch" ]]; then
	exit 0
fi

if [[ "$before_sha" != "$zero_sha" ]] && git cat-file -e "$before_sha^{commit}" 2>/dev/null; then
	git rev-list --reverse "$before_sha..HEAD"
	exit 0
fi

# For a new branch, exclude commits reachable from other tracking
# refs on origin, but not the just-created destination ref itself.
exclusions=()
while read -r tracking_ref; do
	[[ "$tracking_ref" == "refs/remotes/origin/${pushed_ref#refs/heads/}" ]] && continue
	exclusions+=("$tracking_ref")
done < <(git for-each-ref --format='%(refname)' refs/remotes/origin)
if ((${#exclusions[@]} > 0)); then
	git rev-list --reverse HEAD --not "${exclusions[@]}"
else
	git rev-list --reverse HEAD
fi
