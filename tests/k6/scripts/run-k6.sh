#!/usr/bin/env bash

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# CI / interactive runner for kacho-nlb k6 scenarios.
#
# Usage:
#   scripts/run-k6.sh smoke
#   scripts/run-k6.sh baseline --quiet
#   scripts/run-k6.sh all                 # smoke + baseline (NOT stress/soak)
#
# Reads env from .env if present (gitignored).

set -euo pipefail

SCENARIO="${1:-smoke}"
shift || true

K6_BIN="${K6_BIN:-k6}"
K6_BASE_URL="${K6_BASE_URL:-http://localhost:18080}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUT_DIR="${K6_OUT_DIR:-$ROOT_DIR/results}"
mkdir -p "$OUT_DIR"

run_one() {
  local scenario="$1"
  local stamp
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  local script="$ROOT_DIR/scenarios/${scenario}.js"
  if [[ ! -f "$script" ]]; then
    echo "unknown scenario: $scenario" >&2
    return 2
  fi
  echo "==> running k6 scenario: $scenario  (base=$K6_BASE_URL  out=$OUT_DIR)"
  "$K6_BIN" run \
    --env "K6_BASE_URL=$K6_BASE_URL" \
    --out "json=$OUT_DIR/${scenario}-${stamp}.json" \
    "$@" \
    "$script"
}

case "$SCENARIO" in
  all)
    run_one smoke "$@"
    run_one baseline "$@"
    ;;
  smoke|baseline|stress|soak|spike)
    run_one "$SCENARIO" "$@"
    ;;
  *)
    echo "usage: $0 {smoke|baseline|stress|soak|spike|all} [k6-flags...]" >&2
    exit 2
    ;;
esac
