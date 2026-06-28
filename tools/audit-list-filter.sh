#!/usr/bin/env bash

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# audit-list-filter.sh — CI gate (RBAC sub-phase D §11 / issue #111) for kacho-nlb.
#
# Refuses to ship a public `List<Resource>` use-case that returns rows without
# consulting `authzfilter.Filter` (the canonical per-object RBAC list-filter port
# wrapped around kacho-iam AuthorizeService.ListObjects). Per §11 / security.md,
# every public List<Resource> RPC must filter results per-object (read==enforce,
# fail-closed, no-leak).
#
# nlb layout: handlers are colocated per resource package
# (internal/apps/kacho/api/<res>/), and the list-filter is wired in the List
# use-case file (<res>/list.go), not a separate handler dir. Heuristic:
#   1. Collect every internal/apps/kacho/api/<res>/list.go.
#   2. For each, require BOTH `authzfilter.Filter` (field on the use-case) AND
#      `authzfilter.Resolve(` (the per-object filter call).
#   3. If either token is missing, print the candidate path and exit 1.
#
# Override:
#   tools/audit-list-filter.sh --allow="<res>" extends the whitelist (admin-only
#   / catalog-style List where every authenticated caller sees every row).

set -euo pipefail

WHITELIST=()
while [[ ${1:-} == --allow=* ]]; do
  WHITELIST+=("${1#--allow=}")
  shift || true
done

is_whitelisted() {
  local r=$1
  for w in "${WHITELIST[@]:-}"; do [[ "$w" == "$r" ]] && return 0; done
  return 1
}

ROOT=internal/apps/kacho/api
if [[ ! -d "$ROOT" ]]; then
  echo "audit-list-filter: not in kacho-nlb (no $ROOT)" >&2
  exit 0
fi

FAIL=0
for file in "$ROOT"/*/list.go; do
  [[ -e "$file" ]] || continue
  res=$(basename "$(dirname "$file")")
  is_whitelisted "$res" && continue
  if grep -q 'authzfilter\.Filter' "$file" && grep -q 'authzfilter\.Resolve(' "$file"; then
    continue
  fi
  echo "audit-list-filter: $res — List use-case missing authzfilter wiring"
  echo "  file: $file"
  FAIL=1
done

if [[ $FAIL -ne 0 ]]; then
  echo
  echo "RBAC sub-phase D §11 (issue #111) requires every public List<Resource>"
  echo "RPC to filter results per-object through authzfilter.Filter"
  echo "(kacho-iam AuthorizeService.ListObjects backend, relation viewer)."
  echo "Whitelist the resource (admin-only / catalog) with --allow=<res>"
  echo "if the bypass is intentional."
  exit 1
fi

echo "audit-list-filter: OK"
