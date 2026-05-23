// Package loadbalancer — doc-only stub-пакет (placeholder под service-слой).
//
// Port-интерфейсы CQRS-репо для NetworkLoadBalancer живут в leaf-пакете
// `internal/repo/kacho` (LoadBalancerReaderIface / LoadBalancerWriterIface),
// чтобы избежать import-cycle: dto/type2pb → repo/kacho → domain.
//
// Этот пакет может в будущем содержать service-specific port-интерфейсы для
// use-case'ов (если они захотят сузить требования к репо до подмножества
// методов) — но базовый Reader/Writer-контракт фиксирован в leaf-пакете.
package loadbalancer
