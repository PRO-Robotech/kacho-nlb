// Package loadbalancer — gRPC handler + use-cases для NetworkLoadBalancerService.
//
// evgeniy §2.B: handler — тонкий transport (parse-request → call use-case → format-response),
// use-cases — per-RPC файлы (create.go / update.go / delete.go / start.go / stop.go /
// move.go / attach_target_group.go / detach_target_group.go / get_target_states.go /
// list_operations.go).
//
// Каждый use-case принимает domain-тип (НЕ proto-message напрямую) и репозиторий через
// CQRS Repository interface (Reader/Writer split).
//
// TODO(KAC-153..KAC-157): полная реализация всех 10 RPC согласно acceptance §4-§5.
package loadbalancer
