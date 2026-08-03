#!/usr/bin/env bash
# Fake alass stub for unit tests: writes only to stderr and exits 1, to
# prove the wrapper never treats stderr as alass's diagnostic channel (real
# alass writes error text to stdout only).
set -euo pipefail

echo "this line must never appear in a wrapped error" >&2
exit 1
