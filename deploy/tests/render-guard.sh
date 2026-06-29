#!/usr/bin/env bash

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# render-guard.sh — offline Helm render assertions for the kacho-nlb deploy chart.
#
# Pure `helm template` / `helm lint` rendering — никогда не контактирует с
# кластером (read-only chart-render). Гейтит cross-service peer-edge wiring в
# config.yaml (ConfigMap) и mTLS-секции: для каждого ребра (vpc/compute/iam/geo)
# проверяется, что addr и (при включённом per-edge mTLS) serverName/enable
# действительно рендерятся. Источник истины ожиданий — values.yaml + configmap.yaml.
#
# Использование:
#   deploy/tests/render-guard.sh            # из корня репо или из deploy/
#   (helm должен быть в PATH; кластер не нужен)
#
# Выход: 0 — все ассершены прошли; 1 — есть провал (печатает ❌ и diff-контекст).
set -euo pipefail

# Корень chart'а: скрипт лежит в deploy/tests/, chart — в deploy/.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

FAIL=0
pass() { printf '  ✅ %s\n' "$1"; }
fail() { printf '  ❌ %s\n' "$1"; FAIL=1; }

# render <extra helm --set args...> — печатает rendered manifests в stdout.
render() {
  helm template testrel "$CHART_DIR" --set db.password=test "$@" 2>/dev/null
}

# assert_contains <render-output> <needle> <description>
assert_contains() {
  local out="$1" needle="$2" desc="$3"
  if printf '%s' "$out" | grep -qF -- "$needle"; then
    pass "$desc"
  else
    fail "$desc — ожидали подстроку: $needle"
  fi
}

# assert_absent <render-output> <needle> <description>
assert_absent() {
  local out="$1" needle="$2" desc="$3"
  if printf '%s' "$out" | grep -qF -- "$needle"; then
    fail "$desc — НЕ ожидали подстроку, но нашли: $needle"
  else
    pass "$desc"
  fi
}

echo "==> render-guard: peer-edge wiring (config.yaml + mTLS)"

# ─── 1. extapi peer-addrs всегда присутствуют (вне зависимости от mTLS) ────────
echo "[extapi peer addrs — всегда в config.yaml]"
OUT_PLAIN="$(render)"
assert_contains "$OUT_PLAIN" 'addr: "kacho-vpc.kacho.svc.cluster.local:9090"'      "vpc extapi addr рендерится"
assert_contains "$OUT_PLAIN" 'addr: "kacho-compute.kacho.svc.cluster.local:9090"'  "compute extapi addr рендерится"
assert_contains "$OUT_PLAIN" 'addr: "kacho-iam.kacho.svc.cluster.local:9090"'      "iam extapi addr рендерится"
# geo — новое ребро nlb→geo (epic kacho-geo). Паритет с compute: addr на public :9090.
assert_contains "$OUT_PLAIN" 'addr: "kacho-geo.kacho.svc.cluster.local:9090"'      "geo extapi addr рендерится"

# ─── 2. per-edge mTLS: compute (reference) ────────────────────────────────────
echo "[mTLS compute edge — reference shape]"
OUT_COMPUTE="$(render --set mtls.enable=true --set mtls.edges.compute=true)"
assert_contains "$OUT_COMPUTE" 'servername: "compute.kacho.svc.cluster.local"' "compute mTLS serverName рендерится при edges.compute=true"

# ─── 3. per-edge mTLS: geo (mirror of compute) ────────────────────────────────
echo "[mTLS geo edge — mirror of compute]"
OUT_GEO="$(render --set mtls.enable=true --set mtls.edges.geo=true)"
# geo addr ОБЯЗАН быть в том же рендере (config.yaml).
assert_contains "$OUT_GEO" 'addr: "kacho-geo.kacho.svc.cluster.local:9090"'        "geo addr присутствует при edges.geo=true"
# geo mTLS-блок: enable + serverName (∈ geo server-SAN).
assert_contains "$OUT_GEO" 'servername: "kacho-geo.kacho.svc.cluster.local"'       "geo mTLS serverName рендерится при edges.geo=true"
# geo-edge client-creds переиспользуют общий nlb-client cert (не выдумываем новый).
assert_contains "$OUT_GEO" 'certfile: "/etc/kacho-nlb/tls/client/tls.crt"'         "geo mTLS использует общий nlb-client cert"

# geo mTLS-блок должен быть именно enable: true (а не дефолтный enable: false).
# В рендере есть ДВА блока `geo:` (extapi.geo и mtls.geo). mTLS-блок — тот, чья
# СЛЕДУЮЩАЯ строка `enable:` (extapi.geo первой строкой имеет addr). Печатаем
# `enable:`, идущий непосредственно за `geo:`.
mtls_geo_enable() {
  printf '%s' "$1" | awk '
    prev_geo && /^[[:space:]]+enable:/ { print; exit }
    { prev_geo = ($0 ~ /^      geo:$/) }'
}
GEO_MTLS_ENABLE="$(mtls_geo_enable "$OUT_GEO")"
if printf '%s' "$GEO_MTLS_ENABLE" | grep -qF 'enable: true'; then
  pass "geo mTLS enable: true при edges.geo=true"
else
  fail "geo mTLS enable должен быть true при edges.geo=true (получили: '${GEO_MTLS_ENABLE:-<пусто>}')"
fi

# ─── 4. geo mTLS off by default (edges.geo=false) — нет лишних creds ──────────
echo "[mTLS geo edge — off by default]"
OUT_GEO_OFF="$(render --set mtls.enable=true)"
GEO_MTLS_ENABLE_OFF="$(mtls_geo_enable "$OUT_GEO_OFF")"
if printf '%s' "$GEO_MTLS_ENABLE_OFF" | grep -qF 'enable: false'; then
  pass "geo mTLS enable: false по умолчанию"
else
  fail "geo mTLS должен быть enable: false по умолчанию (получили: '${GEO_MTLS_ENABLE_OFF:-<пусто>}')"
fi
# При выключенном edge serverName geo НЕ рендерится (gated, как у compute).
assert_absent "$OUT_GEO_OFF" 'servername: "kacho-geo.kacho.svc.cluster.local"' "geo serverName отсутствует при edges.geo=false"

# ─── 5. каждый rendered-объект несёт apiVersion ───────────────────────────────
# Лицензионный хедер (`# SPDX-License-Identifier: …`) перед условным объектом
# склеивается с первой строкой манифеста, если открывающий `{{- if X -}}`
# right-trim'ит перевод строки: `# SPDX…BUSL-1.1apiVersion: v1` — тогда
# `apiVersion` оказывается частью комментария, а документ приходит с `kind:` без
# `apiVersion:`. `helm template` это пропускает, но `helm install` падает с
# «unable to build kubernetes objects … apiVersion not set». Рендерим со ВСЕМИ
# условными объектами и проверяем инвариант «есть kind → есть apiVersion».
echo "[каждый объект несет apiVersion — header/whitespace-trim glue guard]"
OUT_ALL="$(render \
  --set serviceAccount.create=true \
  --set rbac.create=true \
  --set autoscaling.enabled=true \
  --set networkPolicy.enable=true \
  --set serviceMonitor.enable=true)"
BAD_DOCS="$(printf '%s\n' "$OUT_ALL" | awk '
  BEGIN { src="(unknown)"; hasKind=0; hasApi=0 }
  /^---[[:space:]]*$/ {
    if (hasKind && !hasApi) print "    " src;
    src="(unknown)"; hasKind=0; hasApi=0; next
  }
  /^# Source:/ { src=$0 }
  /^kind:/ { hasKind=1 }
  /^apiVersion:/ { hasApi=1 }
  END { if (hasKind && !hasApi) print "    " src }
')"
if [[ -z "$BAD_DOCS" ]]; then
  pass "все rendered-объекты несут apiVersion"
else
  fail "документ(ы) с kind: без apiVersion (header/trim glue):"$'\n'"$BAD_DOCS"
fi

echo
if [[ "$FAIL" -ne 0 ]]; then
  echo "render-guard: FAILED"
  exit 1
fi
echo "render-guard: OK"
