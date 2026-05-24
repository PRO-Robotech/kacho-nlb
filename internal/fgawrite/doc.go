// Package fgawrite — write-side OpenFGA integration helpers for kacho-nlb
// (D-11 sync hierarchy + creator tuple emit; D-13 lifecycle subscribe consumer-side
// lives in internal/clients/iam — separate concern).
//
// On Create of a kacho-nlb resource (`nlb_load_balancer`, `nlb_listener`,
// `nlb_target_group`) the use-case worker MUST publish the per-resource FGA
// hierarchy tuple(s) so that downstream Check requests have a path to the
// tenant project (where the principal's role binding lives). Without those
// tuples a per-resource Get/Update/Delete Check returns fail-closed DENY.
//
// The helpers are best-effort + non-fatal: the resource row is already
// committed by the time these are called, so a tuple-write failure is only
// logged (operator visibility). The retry policy lives inside the underlying
// HierarchyTupleWriter implementation (kacho-corelib/retry on Unavailable).
//
// Three emission sites:
//
//   - EmitCreator         — "<subject> <relation>@<type>:<id>"
//     (e.g. user:usr-1 #owner @nlb_load_balancer:nlb-1) — written when a
//     resource is created with an authenticated principal.
//
//   - EmitParentLink      — "<parentType>:<parentID> <relation>@<childType>:<childID>"
//     (e.g. nlb_load_balancer:nlb-1 #load_balancer @nlb_listener:lst-1) —
//     written when a child resource is created so the FGA cascade
//     `relation from load_balancer` resolves to the LB's project.
//
//   - EmitProjectRewrite  — "project:<projectID> #project @<type>:<id>"
//     (e.g. project:prj-1 #project @nlb_target_group:tgr-1) — written on
//     Create (initial project link) and Move (src→dst rewrite).
//
// Pattern uniform with kacho-vpc/internal/apps/kacho/fgawrite and
// kacho-corelib best-effort emission idioms.
package fgawrite
