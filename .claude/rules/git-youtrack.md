# Git, документооборот (YouTrack KAC), баги

## Git / коммиты

- Conventional Commits (`feat:`/`fix:`/`chore:`/`docs:`/`test:`/`ci:`/`refactor:`).
- Подпись — git-config (`user.name`/`user.email` репо). **НЕ добавлять** `Co-Authored-By` или attribution-trailers (локальный проект).
- **Не пушить в `main` напрямую и не `--force`** (если владелец явно не разрешил) — работа через ветку = номер тикета → PR.
- `--no-verify` (скип pre-commit hooks) — только по явной просьбе.

## Когда заводить тикет (решается В НАЧАЛЕ запроса)

- **Фича** (новый ресурс/API/раздел UI/кросс-репо поведение) → тикет СНАЧАЛА, ветка `KAC-<N>` в каждом
  затронутом репо, PR'ы ссылаются (`Closes/relates KAC-N`), ссылка на PR — комментарием в тикет.
  ~3+ репо или крупно → **эпик** + Subtask'и + в текущий спринт.
- **Фиксы / мелкие UX** → ОДИН batch-тикет на сессию («<area> bugfix batch — YYYY-MM-DD»).
- **Тривия** (опечатка/однострочник/docs) → без тикета, достаточно коммита.
- Существенный кусок без тикета — завести ретроспективно (тривию не бэкфиллить).

## YouTrack KAC

Трекер — проект `KAC` на `https://prorobotech.youtrack.cloud/` (доска `agiles/183-12`).
MCP `mcp__youtrack__*` или REST `…/api/...` с perm-токеном.

- **Эпик** — `[EPIC]` в summary (нет поля Type) + Subtask-иерархия (link `subtask of KAC-<epic>`). Описан: цель, решения, декомпозиция, кросс-репо порядок, DoD.
- **Subtask** — описан (что/DoD/репо/артефакт) + блокеры в тексте. Каждый issue добавляется в текущий спринт (`Board kacho <sprint>`).
- **Роль исполнителя** — поле `агент` (= имя субагента) + строка `**Роль:** <agent>` в описании.
- **States**: `To do` → `In Progress` → `Test` → `Done`. При завершении — в `Done` + ВСЕ артефакты в комментарий (PR-URL, лог тестов, кросс-репо ссылки).
- **Gate**: кодинг таска (вне `kacho-vpc-implement`) — только после APPROVED acceptance-дока под-фазы (`acceptance-reviewer`).

## git-флоу под задачу

- `git checkout -b KAC-<N>` от `main` в каждом затронутом репо (порядок по build-графу).
- `git push -u origin KAC-<N>` → `gh pr create --title "[KAC-<N>] …" --body "… Closes KAC-<N>"` → ссылку комментарием в тикет.
- После merge + `Done` — удалить ветку (`gh pr merge --delete-branch` или `git push origin --delete KAC-<N>` + `git branch -D`). Исключение: ветка нужна зависимой работе.
- KAC-trail в vault (`obsidian/kacho/KAC/KAC-<N>.md`) — обязателен (см. `vault.md`).

## Баги / tech-debt — GitHub Issues (не TODO.md)

- Баг/tech-debt/observability-gap → GitHub Issue в репо, где живёт (общий → `kacho-workspace`). `TODO.md` упразднён.
- Метки: `bug`/`tech-debt`/`enhancement`; `blocked` + `blocked:kacho-<svc>`; `epic`; `wontfix` (с обоснованием).
- Кросс-репо зависимость — `Blocked by PRO-Robotech/<repo>#<n>` в теле.
- Найдено в тестах → issue (`bug`/`tech-debt`); в кейсе — короткая аннотация `# verifies <…>`, не дублировать описание.
- By-design отклонение — не issue, а запись в `docs/architecture/` сервиса. Не путать с feature-acceptance-флоу (новая фича → APPROVED Given-When-Then).
