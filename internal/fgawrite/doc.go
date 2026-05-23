// Package fgawrite — helpers для эмита FGA hierarchy-tuple'ов
// (D-11 sync hierarchy + D-13 lifecycle subscribe consumer-side).
//
// При Create нового ресурса (`nlb_load_balancer`, `nlb_listener`, `nlb_target_group`)
// сервис обязан вызвать `iam.InternalIAMService.WriteCreatorTuple` для добавления
// `user:<actor>#owner@<resource_type>:<id>` + parent hierarchy
// `nlb_listener:<id>#parent@nlb_load_balancer:<lb_id>` (см. design §6.3).
//
// TODO(KAC-152): WriteCreatorTuple / WriteHierarchyTuple helper'ы + retry с
// fallback на outbox-event для at-least-once consistency.
package fgawrite
