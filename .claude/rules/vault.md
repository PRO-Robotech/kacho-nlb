# Obsidian vault — обязательный context-источник и trail

Vault: `kacho-workspace/obsidian/kacho/` — самодостаточные узкие записки 1-3KB,
**источник истины** для cross-repo связей, ресурсной модели, RPC-контрактов,
runtime-графа. Категории: `resources/` · `rpc/` · `packages/` · `edges/` · `KAC/`.
Полные правила тегов/frontmatter — `obsidian/kacho/CLAUDE.md`.

**Чтение** (MCP `obsidian` подключён): `mcp__obsidian__get_vault_file` /
`search_vault_smart` / `list_vault_files`. Fallback — прямой `Read` от `obsidian/kacho/`.
MCP-сервер пишет в собственную vault-директорию (не всегда совпадает с repo-путём) —
обновление vault делай через MCP-tools, не Edit по абсолютному пути.

## ДО кода (читать минимум — 1-2 узких файла, не 50KB README)

| Триггер | Файл |
|---|---|
| Работа над ресурсом `<X>` | `resources/<repo>-<X>.md` — FK contract, lifecycle, gotchas, ID prefix |
| Меняем RPC | `rpc/<repo>-<service>.md` — методы, REST mapping, sync/async |
| Меняем пакет | `packages/<repo>-<pkg>.md` — exported API, imports, imported-by |
| Cross-service interaction | `edges/<caller>-to-<callee>-<purpose>.md` — protocol, sync/async, error, history |
| KAC-тикет в работе | `KAC/KAC-<N>.md` (создать если нет) |
| Не знаю с чего начать | `INDEX.md` / `README.md` |

Нужно > 3 vault-файлов → остановись и переосмысли scope.

## ПОСЛЕ работы (обновить trail — НЕ упускать)

- Изменилась структура ресурса (поле/status/FK/immutable) → `resources/<X>.md`.
- Добавлен/изменён/удалён RPC → `rpc/<service>.md`. Изменился exported API пакета → `packages/<pkg>.md`.
- Изменилось cross-service runtime поведение → `edges/<edge>.md` + запись в "History" с KAC-номером.
- Затронуто поведение, которого нет в vault → создай **новую** узкую запись (1-3KB).
- KAC-тикет → `KAC/KAC-<N>.md`: «Затронутые сущности vault» + PR-URL + status. Формат — см. `obsidian/kacho/CLAUDE.md`.

## KAC-trail (обязателен для каждого тикета)

`obsidian/kacho/KAC/KAC-<N>.md` создаётся при первом упоминании тикета / создании ветки.
Содержит: Status (in-progress|test|done|wontfix), Type, Repos, PRs, YT-ссылку, «Что и зачем»,
«Затронутые сущности vault» (wikilinks на resources/packages/edges/rpc), DoD-чеклист, связанные тикеты.
Обновлять после каждого merge и смены status в YT.

## Запреты

- Не загружать большой `<repo>/CLAUDE.md`, если хватает 1-2 файлов из vault.
- Не оставлять stale-данные (факт устарел → fix сразу).
- Не дублировать содержимое vault в коде/комментариях/commit-messages.
- Не записывать секреты (токены/пароли) — vault git-committed.
- Канонические теги — только из `obsidian/kacho/CLAUDE.md` (новые синонимы ломают Bases/фильтры).
