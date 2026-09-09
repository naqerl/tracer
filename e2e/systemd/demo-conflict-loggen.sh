#!/usr/bin/env bash
set -euo pipefail

# Generates two interleaved JSON log shapes that share message and timestamp
# keys but disagree on the level key (level vs severity). The format guesser
# reports this as an inconsistent level_field conflict so the panel's add-app
# form can exercise its "favour" resolution UI end-to-end.

trap 'exit 0' TERM INT

i=0
while true; do
	ts="$(date -u +%Y-%m-%dT%H:%M:%S.%N)Z"
	printf '%s\n' "{\"time\":\"${ts}\",\"level\":\"debug\",\"msg\":\"conflict-flow-A-${i}\"}"
	ts="$(date -u +%Y-%m-%dT%H:%M:%S.%N)Z"
	printf '%s\n' "{\"time\":\"${ts}\",\"severity\":\"info\",\"msg\":\"conflict-flow-B-${i}\"}"
	i=$((i + 1))
	sleep 0.2
done