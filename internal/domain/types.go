// Package domain — self-validating domain newtypes для kacho-nlb (evgeniy §4.D).
//
// Все поля с семантикой — newtypes с `Validate() error`. Голый `string`
// запрещён (evgeniy §D.2). Domain-пакет импортирует ТОЛЬКО stdlib,
// `H-BF/corlib/pkg/{dict,option}` и `kacho-corelib/errors` — никаких pgx,
// grpc-stubs, sqlc-types; domain не знает adapter'ов (workspace CLAUDE.md
// «Чистая архитектура»).
//
// CreatedAt сюда не входит (DB-managed) — он живёт в repo-сущности
// (evgeniy §H.1).
package domain

import (
	"net/netip"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/H-BF/corlib/pkg/dict"
	"github.com/H-BF/corlib/pkg/option"
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
)

// ---- ID newtypes -----------------------------------------------------------

type (
	// ResourceID — 3-char prefix + 17-char crockford-base32 (corelib/ids).
	// Внутри domain хранится как непрозрачная строка; формат валидируется
	// в service-слое (corevalidate.ResourceID).
	ResourceID string

	// ProjectID — id ресурса kacho-iam Project, "prj" + 17.
	ProjectID string

	// RegionID — semantic id, "ru-central1"-style; owner — kacho-compute.
	RegionID string

	// ZoneID — semantic id, "ru-central1-a"-style; owner — kacho-compute.
	ZoneID string

	// SubnetID, NetworkID, AddressID, NicID, InstanceID — type-aliases для
	// ResourceID. Алиасы (а не distinct newtypes) — потому что они хранят
	// тот же 20-символьный prefix-формат и cross-service refs валидируются
	// в worker-е peer-gRPC-вызовом (а не локально на типе).
	SubnetID   = ResourceID
	NetworkID  = ResourceID
	AddressID  = ResourceID
	NicID      = ResourceID
	InstanceID = ResourceID
)

// ---- Семантические строковые поля ------------------------------------------

type (
	// LbName — strict-name resource (regex `^[a-z][-a-z0-9]{1,61}[a-z0-9]$`,
	// 3..63 chars). NLB следует strict-policy — пустое имя / underscore /
	// uppercase запрещены (acceptance §3 NLB-003 verbatim).
	LbName string

	// LbDescription — UTF-8 длиной ≤ 256.
	LbDescription string

	// LbNameOpt — optional LbName (используется как nullable name-поле, где
	// семантически «не задано» отличается от «пустая строка»).
	LbNameOpt = option.ValueOf[LbName]
)

// ---- Labels (dict.HDict с typed key/value) ---------------------------------

type (
	// LbLabelKey — ключ label (regex `^[a-z][-_./\\@a-z0-9]{0,62}$`).
	LbLabelKey string

	// LbLabelVal — значение label (0..63 байт).
	LbLabelVal string

	// LbLabels — labels-набор; cardinality ≤ MaxLabelPairs.
	LbLabels = dict.HDict[LbLabelKey, LbLabelVal]
)

// ---- Сетевые/численные newtypes --------------------------------------------

type (
	// LbPort — порт TCP/UDP, 1..65535.
	LbPort int32

	// LbProto — protocol listener'а: "TCP" | "UDP" (L4 only).
	LbProto string

	// IPVersion — "IPV4" | "IPV6".
	IPVersion string

	// IPAddress — текстовое представление IP; парсится netip.ParseAddr.
	IPAddress string

	// LbWeight — вес таргета в TG, 0..MaxTargetWeight (0 = "drain без remove").
	LbWeight int32

	// LbDuration — длительность (healthcheck interval/timeout, deregistration
	// delay). Range зависит от поля; валидируется на структуре, не на типе.
	LbDuration time.Duration
)

// ---- Regex -----------------------------------------------------------------

var (
	// strict-name контракт NLB (acceptance §3 NLB-003).
	lbNameRe = regexp.MustCompile(`^[a-z][-a-z0-9]{1,61}[a-z0-9]$`)

	// label-key контракт (зеркалит kacho-vpc).
	lbLabelKeyRe = regexp.MustCompile(`^[a-z][-_./\\@a-z0-9]{0,62}$`)
)

// ---- Validate() ------------------------------------------------------------

// Validate проверяет, что value соответствует strict-name контракту NLB
// (acceptance §3 NLB-003): regex плюс required (пустая строка → отдельная
// ошибка для верности UX — `name is required` вместо regex-mismatch).
func (n LbName) Validate() error {
	if n == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation("name", "name is required").
			Err()
	}
	if !lbNameRe.MatchString(string(n)) {
		return coreerrors.InvalidArgument().
			AddFieldViolation("name",
				`name must match ^[a-z][-a-z0-9]{1,61}[a-z0-9]$ (lowercase letters, digits, hyphens; 3..63 chars; starts with letter; ends with letter or digit)`).
			Err()
	}
	return nil
}

// Validate проверяет длину description (UTF-8 rune count ≤ MaxDescriptionLen).
func (d LbDescription) Validate() error {
	if utf8.RuneCountInString(string(d)) > MaxDescriptionLen {
		return coreerrors.InvalidArgument().
			AddFieldViolation("description", "description length exceeds 256 chars").
			Err()
	}
	return nil
}

// Validate проверяет LabelKey-регекс (1..63 bytes).
func (k LbLabelKey) Validate() error {
	s := string(k)
	if len(s) == 0 || len(s) > MaxLabelKeyLen || !lbLabelKeyRe.MatchString(s) {
		return coreerrors.InvalidArgument().
			AddFieldViolation("labels."+s,
				"invalid label key (1..63 chars, lowercase letters, digits, _-./\\@; must start with letter)").
			Err()
	}
	return nil
}

// Validate проверяет LbLabelVal (0..63 bytes; пустая строка OK).
func (v LbLabelVal) Validate() error {
	if len(string(v)) > MaxLabelValueLen {
		return coreerrors.InvalidArgument().
			AddFieldViolation("labels", "label value exceeds 63 chars").
			Err()
	}
	return nil
}

// ValidateLabels — cardinality ≤ MaxLabelPairs + per-key/value validate.
// Свободная функция (а не метод HDict.Validate) — receiver мы не контролируем.
func ValidateLabels(labels LbLabels) error {
	if labels.Len() > MaxLabelPairs {
		return coreerrors.InvalidArgument().
			AddFieldViolation("labels", "too many labels (max 64)").
			Err()
	}
	var firstErr error
	labels.Iterate(func(k LbLabelKey, v LbLabelVal) bool {
		if err := k.Validate(); err != nil {
			firstErr = err
			return false
		}
		if err := v.Validate(); err != nil {
			firstErr = err
			return false
		}
		return true
	})
	return firstErr
}

// Validate проверяет port-range [PortMin, PortMax].
func (p LbPort) Validate() error {
	if p < PortMin || p > PortMax {
		return coreerrors.InvalidArgument().
			AddFieldViolation("port", "port must be in range [1, 65535]").
			Err()
	}
	return nil
}

// Validate проверяет, что proto ∈ {TCP, UDP} (L4 only — design §2.1).
func (p LbProto) Validate() error {
	switch p {
	case ProtoTCP, ProtoUDP:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("protocol", "protocol must be one of: TCP, UDP").
		Err()
}

// Validate проверяет, что ip_version ∈ {IPV4, IPV6}.
func (v IPVersion) Validate() error {
	switch v {
	case IPVersionV4, IPVersionV6:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("ip_version", "ip_version must be one of: IPV4, IPV6").
		Err()
}

// Validate проверяет, что address парсится netip.ParseAddr.
// Bogon-/public-only policy для target.external_ip — отдельно в Target.Validate
// (там это не «формат IP», а «политика target'а»: design §2.5 / acceptance
// TGR-011 / TGT-001).
func (a IPAddress) Validate() error {
	if a == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation("address", "address is required").
			Err()
	}
	if _, err := netip.ParseAddr(string(a)); err != nil {
		return coreerrors.InvalidArgument().
			AddFieldViolation("address", "invalid IP address").
			Err()
	}
	return nil
}

// Validate проверяет weight ∈ [0, MaxTargetWeight] (acceptance TGT-005).
func (w LbWeight) Validate() error {
	if w < 0 || w > MaxTargetWeight {
		return coreerrors.InvalidArgument().
			AddFieldViolation("weight", "weight must be in range [0, 1000]").
			Err()
	}
	return nil
}

// ---- Helpers для конверсии LbLabels ↔ map[string]string --------------------

// LabelsFromMap конвертирует map[string]string → LbLabels (handler-layer).
// nil-map → пустой LbLabels.
func LabelsFromMap(m map[string]string) LbLabels {
	var d LbLabels
	for k, v := range m {
		d.Put(LbLabelKey(k), LbLabelVal(v))
	}
	return d
}

// LabelsToMap — обратное преобразование для DTO. nil если LbLabels пуст
// (паритет с proto-семантикой: отсутствие labels = поле не задано).
func LabelsToMap(d LbLabels) map[string]string {
	if d.Len() == 0 {
		return nil
	}
	m := make(map[string]string, d.Len())
	d.Iterate(func(k LbLabelKey, v LbLabelVal) bool {
		m[string(k)] = string(v)
		return true
	})
	return m
}

// LabelsEqual — set-equality для LbLabels (used in Update no-op detection).
func LabelsEqual(a, b LbLabels) bool {
	if a.Len() != b.Len() {
		return false
	}
	equal := true
	a.Iterate(func(k LbLabelKey, v LbLabelVal) bool {
		bv, ok := b.Get(k)
		if !ok || bv != v {
			equal = false
			return false
		}
		return true
	})
	return equal
}
