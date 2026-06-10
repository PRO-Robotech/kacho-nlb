---
name: kacho-docs-writer
description: Регламент написания/правки документации Kachō — per-service docs-site (Docusaurus 3), спека-книга docs/specs/00-04, per-repo docs/architecture, README. Применять при любой задаче «написать/обновить/вычитать документацию»; кодифицирует own-product тон (без сравнений с чужими облаками), сверку фактов с ground-truth, валидность MDX/mermaid, build-гейт (0 broken links) и связность глав. Vault-записки — НЕ сюда (это .claude/rules/vault.md).
---

# Skill: kacho-docs-writer — документация Kachō

Выработан на полном цикле приведения доков к продакшн-виду (de-YC всей оснастки,
перепись спеки 00–04, два прогона вычитки `kacho-vpc/docs-site`). Это нормативный
регламент: пиши документацию ТАК, отклонение — осознанно и с обоснованием.

## 0. Когда применять / когда НЕТ

**Применять**: любая работа над `docs-site/` (Docusaurus), `docs/specs/`,
`docs/architecture/` сервиса, README репо; вычитка/актуализация существующих доков.

**НЕ сюда**: vault-записки (`obsidian/kacho/` — правила в `.claude/rules/vault.md`);
godoc/комментарии в коде (skill `evgeniy` + go-style-reviewer); commit-messages
(`.claude/rules/git-youtrack.md`); acceptance-доки (`acceptance-author` — это gate-артефакт,
а не документация).

## 1. Карта слоёв документации (кто источник истины)

| Слой | Где | Роль | Источник истины для |
|---|---|---|---|
| Спека-книга | `kacho-workspace/docs/specs/00-04` | замысел: scope, контракт-намерения, процесс | продукт/scope/конвенции |
| Acceptance-трейл | `docs/specs/*-acceptance.md` | point-in-time APPROVED гейты | историю решений — **НЕ редактировать** |
| docs-site | `project/<svc>/docs-site/` (Docusaurus 3) | публичная дока сервиса: API + архитектура + install | внешнее описание сервиса |
| docs/architecture | `project/<svc>/docs/architecture/` | внутренние by-design решения сервиса | «почему так сделано» |
| Vault | `obsidian/kacho/` | узкие 1-3KB записки для AI-контекста | cross-repo связи (см. vault.md) |
| README | корень репо | вход: что это, как поднять | onboarding |

**Не дублировать между слоями** — ссылаться. Дубль = будущий drift (доказано:
per-repo копии agents разъехались и обросли мусором; `docs/architecture` workspace
удалён именно как дубль specs+vault).

## 2. Непреложные принципы

1. **Own-product.** Kachō — собственный продукт. ЗАПРЕЩЕНЫ сравнения/отсылки к чужим
   облакам (ban #2): не только `yandex`/`YC`/`AWS`, но и НЕЯВНЫЕ формы — «аналог X»,
   «в отличие от классических облаков», «привычный по…», «ENI-подобный», «verbatim»,
   «parity». Дизайн-решения подаются как собственные с обоснованием «зачем», не как
   «расхождение с эталоном». Паттерн: страница «Особенности дизайна Kachō VPC»
   (таблица «Решение → Почему так»), а не «Known divergences from …».
2. **Факты — только из ground-truth.** Не выдумывать и не писать по памяти. Источники
   по приоритету: per-repo `CLAUDE.md` → `.claude/rules/*` → код/миграции/proto.
   Неизвестно → не писать (точность > полнота).
3. **Терминология единая**: project / `projectId` / project-level (НЕ folder);
   иерархия Account → Project; flat-resource (без envelope `metadata/spec/status`);
   мутации → async `Operation` + поллинг `OperationService.Get` (Watch не существует);
   «конвенции Kachō» (не «стиль X»).
4. **Язык** — русский; идентификаторы/код/пути/имена RPC — как есть, не переводить.
5. **Стиль 2026**: лаконично, активный залог, без воды и витиеватости; скан-абельность
   (таблицы для перечислений, списки, fenced-блоки); вводный абзац страницы отвечает
   «что это и зачем читать»; Diátaxis-ориентация (reference / how-to / explanation
   не смешивать в одной секции).

## 3. Workflow правки (любой слой)

1. **Scope**: какой слой, какие страницы, что триггер (новый RPC / ресурс / рефактор).
2. **Ground-truth первым**: прочитай per-repo `CLAUDE.md` (+ узкий vault-файл ресурса,
   если он есть) ДО открытия дока. Факты собираются здесь, не сочиняются в процессе.
3. **Пиши/правь** по принципам §2 и слойному регламенту (§4–§6). Правки в выверенный
   текст — точечные (Edit), не переписывание целиком без необходимости.
4. **Self-check** (обязателен, см. §7) + валидация слоя (§4.5 для docs-site).
5. **Коммит**: `docs(<scope>): …` (Conventional Commits, без attribution-trailers).

## 4. docs-site (Docusaurus 3) — регламент

Эталон — `project/kacho-vpc/docs-site/`. Новый сервисный docs-site строить по нему.

### 4.1 Структура
```
docs-site/
├── docusaurus.config.ts        # title 'Kachō <Svc>', RU locale
├── sidebars.ts                 # единственный источник навигации
├── docs/
│   ├── intro.mdx               # что за сервис, ресурсы, ID-префиксы, статус миграций
│   ├── api/                    # overview + страница на КАЖДЫЙ ресурс + operations
│   ├── architecture/           # overview, data-model, operations, ipam?, authz
│   ├── install/                # deploy, configuration
│   └── advanced/               # observability, design-decisions
└── src/
    ├── components/commonBlocks/  # ApiOperation, Codes, Restrictions, StatusTable
    └── constants/                # codes.ts, restrictions.ts, dictionary.ts, database-schema.ts
```

### 4.2 Данные — через `src/constants`, не инлайн
Коды ошибок, ограничения полей, словарь терминов, схема БД — в TS-константах,
страницы рендерят их компонентами (`<Codes/>`, `<Restrictions/>`, `<StatusTable/>`,
`<ApiOperation/>`). Менять контракт → править константу, не N страниц.
TS-валидность обязательна (типы/экспорты не ломать).

### 4.3 Страница API-ресурса (форма)
Frontmatter (`id`, `title`, `sidebar_position`) → 1-2 строки «что за ресурс» →
поля (таблица: имя / тип / mutable?/output-only) → методы через `<ApiOperation/>`
(Get/List sync; Create/Update/Delete + `:verb` → Operation) → ограничения
(`<Restrictions/>`) → ошибки (`<Codes/>`, канонические тексты — точные строки из
CLAUDE.md, они часть контракта) → preconditions/FK (что блокирует Delete).

### 4.4 mermaid
Парные `subgraph`/`end`; id нод — латиница без пробелов/кавычек; клиенты в
диаграммах — «CLI / UI / SDK» / «Внешние клиенты» (не имя чужого CLI). После правки
диаграммы — build (§4.5), это единственная реальная проверка.

### 4.5 Гейт перед коммитом (обязателен)
```bash
cd docs-site && npm run build   # зелёный + 0 broken links — иначе не коммитить
```
Build ловит сломанный MDX/JSX, незакрытый mermaid и битые внутренние ссылки.
Внутренние ссылки — только на существующие страницы (карта = `sidebars.ts`).
`build/` и `.docusaurus/` — в gitignore, не коммитить.

## 5. Спека-книга `docs/specs/00-04` — регламент

Роли глав фиксированы (single source of truth, дубли запрещены):
- **00** обзор/scope/принципы · **01** сервисы+API-контракт · **02** модель
  данных/naming/error/ID-prefix/схемы (единственный дом этих таблиц) ·
  **03** deploy/ops/CI/оснастка · **04** roadmap/процесс (acceptance-first, TDD).

Каждая глава: шапка `# Kachō — <Название>` + `**Документ:** NN / 5`; открывается
мостом от предыдущей (1-2 строки), закрывается переходом к следующей. Изменил
контракт в 02 → проверь, что 01 не противоречит (ссылка, не копия).
`*-acceptance.md` — исторические APPROVED-гейты: не редактировать, не «актуализировать».

## 6. Чек-лист сверки фактов (что чаще всего врёт)

| Факт | Сверять с |
|---|---|
| ID-префиксы (vpc: `net/sub/adr/rtb/sgr/gtw/nic/apl`, Operation `enp`; compute: `epd`/`fd8`; iam: `acc/prj/usr/sva/grp/rol/acb`, Operation `iop`) | per-repo CLAUDE.md §prefixes |
| Канонические error-тексты (`"<Resource> %s not found"`, `"Subnet CIDRs can not overlap"`, …) | CLAUDE.md §error-mapping — цитировать ТОЧНО |
| Статус-enum'ы ресурсов | CLAUDE.md / proto |
| FK/RESTRICT-цепочки (vpc: NIC → Address → Subnet → Network) | CLAUDE.md §FK contract |
| REST-пути и `:verb`-actions | api-conventions.md |
| Номера/состав миграций | `internal/migrations/` |
| Env-переменные (`KACHO_<SVC>_*`) | config сервиса |
| Internal-vs-public поверхность (AddressPool, Region/Zone — только :9091) | security.md |

## 7. Self-check перед коммитом (любой слой)

```bash
# запрещённое (допустим только CI-гейт verify-no-yandex и имена исторических миграций):
grep -rniE 'yandex|\bYC\b|yc-|\bAWS\b|parity|verbatim|\bfolder\b|upsert|resourceVersion|finalizer|Watch Hub' <изменённые файлы>
```
Плюс: неявные сравнения (§2.1) глазами; внутренние ссылки существуют; для docs-site — §4.5.

## 8. Анти-паттерны (реально пойманные в Kachō)

- **Рамка сравнения** («Known divergences from X», «verbatim-X контракт» в JSDoc
  компонента) — переосмыслить как собственные решения, включая комментарии в коде сайта.
- **Выдуманные факты**: в `intro.mdx` стояли НЕВЕРНЫЕ ID-префиксы (`enp/e9b` вместо
  `net/sub/…`) — написаны по памяти, не по CLAUDE.md. Всегда §6.
- **Stale-глава при живом соседе**: 00 переписали, 01–04 остались со старым контрактом
  (upsert/Watch/envelope) — книга противоречила сама себе. Правишь контракт → проходи
  ВСЕ главы/страницы, где он упомянут (grep по ключам §7).
- **Битая ссылка на удалённую страницу** (`/architecture/conventions`) — после любого
  удаления/переименования страницы: grep по старому пути + build.
- **Дубль overview между слоями** (workspace docs/architecture дублировал specs) —
  один владелец, остальные ссылаются.
- **50KB README** — разбивать; вход должен читаться за минуты.
- **Правка acceptance-доков задним числом** — запрещено, это трейл.

## 9. Смежные роли

Generic-методология doc-систем — skill `code-documenter` (Docusaurus/MkDocs справка).
Канонические error-тексты и REQ-* — `<svc>-conventions-auditor` (аудит) и
`tests/newman/docs/PRODUCT-REQUIREMENTS.md` (нормативный реестр). Тестовая
документация (RESULTS.md, CASES-INDEX) — `<svc>-newman-author`.
