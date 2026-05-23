// Package dto — generic DTO registry (evgeniy §3.C).
//
// Generic Interface + tag-based registry для type-safe transfer'ов между
// слоями. Используется domain ↔ proto (internal/dto/type2pb/) и
// domain ↔ DB-row (internal/repo/kacho/pg/dto/).
//
// TODO(KAC-150): полная реализация согласно kacho-vpc/internal/dto/base.go
// (Generic Interface[FromType, ToType] + RegTransfer/FindTransfer + Fn2Face helper).
package dto

// Interface[F,T] — основная абстракция трансфера.
type Interface[FromType any, ToType any] interface {
	Transfer(FromType) (ToType, error)
}
