#!/usr/bin/env bash

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# tests/newman/scripts/run-incremental.sh — quota-safe per-folder newman runner.
#
# Iterates each case-folder (Postman folder) inside every collection,
# runs newman with --folder, captures per-case JSON for aggregation.
# Designed for environments with tight per-RPC quotas (e.g. early dev stand
# without full AddressPool capacity) where running the entire suite in one shot
# would exhaust resources.
#
# Usage:
#   ./scripts/run-incremental.sh                          # all services
#   ./scripts/run-incremental.sh --service load-balancer
#   ./scripts/run-incremental.sh --resume                 # skip cases already in out/inc/*.json
#   ./scripts/run-incremental.sh --env environments/kind-stand.postman_environment.json

set -euo pipefail
cd "$(dirname "$0")/.."

SERVICE=""
ENV="environments/local.postman_environment.json"
RESUME=0
DELAY="15"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) SERVICE="$2"; shift 2 ;;
    --env)     ENV="$2"; shift 2 ;;
    --resume)  RESUME=1; shift ;;
    --delay)   DELAY="$2"; shift 2 ;;
    *) echo "unknown arg: $1"; exit 1 ;;
  esac
done

[[ -f "$ENV" ]] || { echo "missing env: $ENV"; exit 1; }
mkdir -p out/inc

run_folder() {
  local svc="$1"
  local folder="$2"
  local out_json="out/inc/${svc}__${folder//[^a-zA-Z0-9_-]/_}.json"
  if [[ "$RESUME" == "1" && -f "$out_json" ]]; then
    echo "[skip-resume] $svc / $folder"
    return 0
  fi
  echo "--- $svc / $folder ---"
  newman run "collections/${svc}.postman_collection.json" \
    -e "$ENV" \
    --folder "$folder" \
    --delay-request "$DELAY" \
    --reporters cli,json \
    --reporter-json-export "$out_json" \
    || true
}

list_folders() {
  local col="$1"
  jq -r '.item[].name' "$col"
}

services_to_run=()
if [[ -n "$SERVICE" ]]; then
  services_to_run+=("$SERVICE")
else
  for svc in load-balancer listener target-group targets operation authz-deny; do
    services_to_run+=("$svc")
  done
fi

for svc in "${services_to_run[@]}"; do
  col="collections/${svc}.postman_collection.json"
  if [[ ! -f "$col" ]]; then
    echo "[skip] $svc — no collection"
    continue
  fi
  while IFS= read -r folder; do
    [[ -z "$folder" ]] && continue
    run_folder "$svc" "$folder"
  done < <(list_folders "$col")
done

echo
echo "===== Per-folder summary ====="
{
  printf "%-60s %8s %8s\n" "CASE" "ASSERT" "FAILED"
  for f in out/inc/*.json; do
    [[ -f "$f" ]] || continue
    name=$(basename "$f" .json)
    stats=$(jq -r '"\(.run.stats.assertions.total) \(.run.stats.assertions.failed)"' "$f" 2>/dev/null || echo "0 0")
    set -- $stats
    printf "%-60s %8s %8s\n" "$name" "$1" "$2"
  done
} | tee out/inc-summary.txt
