# kacho-nlb — 09-fga-model

TODO(KAC-168): архитектурное описание секции `09-fga-model` (см. acceptance §1..§16 и design doc 2026-05-23-kacho-nlb-design.md).

## Mirror-feed на label-Update (by-design, sub-phase T3.1 / #113)

Consumer-обязанность: каждый label-selectable nlb-ресурс эмитит
`InternalIAMService.RegisterResource` (mirror.upsert) с **актуальными labels +
parent_project_id** не только на Create, но и на label-меняющем Update — иначе
IAM `resource_mirror` протухает и ARM_LABELS-грант (γ `matchLabels`-селектор) не
ревокается при снятии/смене метки (root-cause #113).

- **listener — двойной баг (исправлен T3.1):** `listenerRegisterIntent`
  (`listener/create.go`) ранее эмитил bare-intent без `Labels` → селектор не
  матчил даже свежесозданный listener. Теперь Create-intent несёт
  `Labels: domain.LabelsToMap(...)` + `ParentProjectID` (parity с
  `lbMirrorIntent`/`tgMirrorIntent`). Update получил `listenerLabelsInMask`-gated
  emit `listenerMirrorIntent` (parent-link tuple + текущие labels) в writer-tx.
- **G-2 (gate):** эмит только когда labels в маске (empty mask = full-PATCH ⇒
  true). Non-label Update (rename/desc) → no-op (меньше reconcile-шума;
  external-поведение идентично always-emit за счёт `source_version`-monotonic).
- **G-3 (upsert, НЕ Unregister):** полное снятие меток (`labels={}`) эмитит
  `RegisterResource` (mirror.upsert) с пустым labels-map — НЕ `UnregisterResource`.
  Ресурс жив, mirror-строка остаётся; пустые labels корректно протухают
  label-селекторы, не снося owner-tuple/containment. `UnregisterResource` —
  только на Delete ресурса.
- **G-4 (atomicity, SEC-D):** mirror-intent пишется в той же writer-tx, что и
  UPDATE listener'а (один `w.Commit`); rollback Update ⇒ intent не записан (нет
  dual-write). Дрейн в IAM — отдельный at-least-once register-drainer.
- **LB / TargetGroup** уже корректны (эталон `labelsInMask`/`labelsInMaskTG`) —
  T3.1 их код не трогал (non-regression).
- **Ребро не новое:** `nlb→iam RegisterResource` существует (SEC-A owner-tuple);
  T3.1 увеличивает частоту эмита (Update-trigger), не меняет payload-форму и не
  вводит iam→nlb обратного вызова (циклов нет).
