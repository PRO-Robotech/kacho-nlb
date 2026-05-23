// Package domain — self-validating domain newtypes для kacho-nlb (evgeniy §4.D).
//
// Все поля с семантикой — newtypes с `Validate() error`. Голый `string`
// запрещён (evgeniy §D.2). Domain-пакет импортирует ТОЛЬКО stdlib;
// никаких pgx, grpc-stubs, sqlc-types — domain не знает adapter'ов
// (workspace CLAUDE.md "Чистая архитектура").
package domain

import "time"

// TODO(KAC-147): полный список newtype'ов согласно design §2.1.
//   - ResourceID, ProjectID, RegionID, ZoneID, SubnetID, NetworkID, AddressID,
//     NicID, InstanceID.
//   - LbLabelKey/Val/Labels (dict.HDict from H-BF/corlib), LbName, LbDescription.
//   - LbPort (1-65535), LbProto ("TCP"|"UDP"), IPVersion, IPAddress, LbWeight, LbDuration.
//   - LBType, LBStatus, ListenerStatus, TargetGroupStatus, TargetStatus, SessionAffinity.

type (
	// ResourceID — 3-char-prefix + 17-char crockford-base32 (см. corelib/ids).
	ResourceID string

	// ProjectID — id-prefix "prj" + 17.
	ProjectID string

	// RegionID — semantic id, "ru-central1"-style.
	RegionID string

	// LbName — 3-63 chars; regex проверяется в Validate().
	LbName string

	// LbDescription — длина ≤256.
	LbDescription string

	// LbPort — 1-65535.
	LbPort int32

	// LbProto — "TCP" | "UDP".
	LbProto string

	// IPVersion — "IPV4" | "IPV6".
	IPVersion string

	// LbDuration — semantic duration (e.g. healthcheck interval).
	LbDuration time.Duration
)

// TODO(KAC-147): Validate() error для каждого newtype.
// TODO(KAC-147): builders.go — NewLoadBalancer/NewListener/NewTargetGroup/NewTarget factory'ы.
// TODO(KAC-147): constants.go — статус-enum константы, magic numbers.
// TODO(KAC-147): errors.go — sentinel ошибки domain-level (`ErrInvalidLabel`, ...).
