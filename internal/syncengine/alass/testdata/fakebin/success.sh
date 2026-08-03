#!/usr/bin/env bash
# Fake alass stub for unit tests: mimics a successful run. Prints a
# realistic "shifted block" summary line to stdout (real alass output has
# no machine-readable mode), records the argv it received to
# $FAKE_ALASS_ARGS_FILE for assertions, copies the incorrect-sub-file
# (positional arg 2) to the output-file-path (positional arg 3) to stand in
# for a real alignment, and exits 0.
set -euo pipefail

echo "shifted block of 1 subtitles with length 00:00:01.000 by -00:00:02.500"

if [[ -n "${FAKE_ALASS_ARGS_FILE:-}" ]]; then
	printf '%s\n' "$@" >"$FAKE_ALASS_ARGS_FILE"
fi

cp "$2" "$3"
exit 0
