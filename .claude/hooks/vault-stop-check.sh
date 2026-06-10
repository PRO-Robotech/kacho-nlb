#!/usr/bin/env bash
# Stop hook — проверка vault state перед окончанием session.
# Корень workspace берём из $CLAUDE_PROJECT_DIR (выставляет Claude Code в hook-env);
# fallback — каталог на 2 уровня выше скрипта (.claude/hooks/ → workspace root).
ROOT="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
VAULT="$ROOT/obsidian/kacho"
PROJ="$ROOT/project"

# 1. Активные KAC-тикеты в vault — напоминание про status update
INPROG=$(grep -rlE "^status: (in-progress|test)" "$VAULT/KAC/" 2>/dev/null | head -5)
if [ -n "$INPROG" ]; then
  echo
  echo "⚠️  АКТИВНЫЕ KAC-ТИКЕТЫ В VAULT (status: in-progress|test):"
  echo "$INPROG" | xargs -I{} basename {} .md | sed 's/^/   • /'
  echo "   → Если PR merged: переведи status: done + обнови «Затронутые сущности vault»."
fi

# 2. Активность за последний час: код-changes vs vault-changes
RECENT_CODE=$(find "$PROJ" \( -name "*.go" -o -name "*.sql" -o -name "*.proto" \) -mmin -60 2>/dev/null | wc -l)
RECENT_VAULT=$(find "$VAULT" -name "*.md" -mmin -60 2>/dev/null | wc -l)
if [ "$RECENT_CODE" -gt 0 ] && [ "$RECENT_VAULT" -eq 0 ]; then
  echo
  echo "⚠️  $RECENT_CODE code-files изменено за час, $RECENT_VAULT vault-файлов."
  echo "   → Если затронут ресурс/RPC/пакет/runtime-edge — обнови соответствующий узкий файл."
fi

# 3. Open PR'ы по KAC-эпикам — проверить нужно ли trail обновить
if command -v gh >/dev/null 2>&1; then
  for repo in kacho-vpc kacho-deploy kacho-compute kacho-iam kacho-api-gateway kacho-corelib kacho-proto; do
    if [ -d "$PROJ/$repo/.git" ]; then
      OPEN=$(cd "$PROJ/$repo" && gh pr list --state open --json number,title 2>/dev/null | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin)
  for p in d:
    if 'KAC-' in p.get('title',''):
      n=p.get('number','?'); t=p.get('title','')[:60]
      print(f'   • $repo#{n}: {t}')
except: pass
" 2>/dev/null)
      [ -n "$OPEN" ] && { [ -z "$HEADER_SHOWN" ] && echo && echo "📂 OPEN PR'Ы С KAC-ТИКЕТАМИ:" && HEADER_SHOWN=1; echo "$OPEN"; }
    fi
  done
fi

exit 0
