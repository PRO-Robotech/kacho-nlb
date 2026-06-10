#!/usr/bin/env bash
# UserPromptSubmit hook — напоминает Claude про vault discipline.
# Выводится перед обработкой каждого user prompt'а.
cat <<'EOF'

📔 KACHO VAULT DISCIPLINE (workspace CLAUDE.md §«Obsidian vault — обязательно»)
─────────────────────────────────────────────────────────────────────────────
ДО кода:
  • Прочитай узкий файл (1-3KB) из obsidian/kacho/{resources,rpc,packages,edges}/
  • НЕ загружай 50KB per-repo README — используй INDEX.md если не знаешь куда смотреть
  • KAC-<N> в работе → открой obsidian/kacho/KAC/KAC-<N>.md (создай если нет)

ПОСЛЕ работы:
  • Обнови resources/<X>.md (поля/FK/lifecycle/gotchas)
  • Обнови rpc/<service>.md (methods/REST mapping)
  • Обнови packages/<repo>-<pkg>.md (exported types/imports)
  • Обнови edges/<edge>.md (если cross-service runtime изменился)
  • Обнови KAC/KAC-<N>.md «Затронутые сущности vault» + PR-URL + status

ЗАПРЕТЫ:
  • НЕ оставляй KAC-trail без обновления после merge
  • НЕ записывай секреты в vault (git-committed)
  • НЕ дублируй overview между 50KB README и узкими 3KB файлами
─────────────────────────────────────────────────────────────────────────────
EOF
