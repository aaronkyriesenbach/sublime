#!/usr/bin/env bash
# Fake alass stub for unit tests: mimics a failed run. Real alass prints its
# only error diagnostics to stdout (never stderr) as a chained
# "error: ... / caused by: ..." message, then exits 1 with no per-category
# codes.
set -euo pipefail

echo "error: could not parse incorrect subtitle file"
echo "caused by: unexpected token at line 4"
exit 1
