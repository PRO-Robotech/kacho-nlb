package domain

import (
	"net/netip"

	"github.com/H-BF/corlib/pkg/option"
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"go.uber.org/multierr"
)

// Target — 4-way oneof identity (design §2.5):
//
//	(1) InstanceID  — in-cloud via kacho-compute (worker resolves primary NIC IP);
//	(2) NicID       — in-cloud via kacho-vpc.NetworkInterface;
//	(3) IPRef       — in-cloud raw IP внутри Subnet (sync IP ∈ CIDR check в worker);
//	(4) ExternalIP  — out-of-cloud raw IP (sync bogon-check в Validate ниже).
//
// Ровно одна из четырёх идентичностей должна быть задана (acceptance TGR-009/010).
// Storage-side enforce — partial UNIQUE NULLS NOT DISTINCT (acceptance TGT-002/003).
type Target struct {
	InstanceID option.ValueOf[InstanceID]
	NicID      option.ValueOf[NicID]
	IPRef      *TargetIPRef
	ExternalIP *TargetExternalIP
	Weight     LbWeight
}

// TargetIPRef — variant (3): IP-адрес внутри какого-то Subnet'а kacho-vpc.
// IP ∈ CIDR проверяется в worker'е (нужен Subnet.cidr_blocks из VPC).
type TargetIPRef struct {
	SubnetID SubnetID
	Address  IPAddress
}

// TargetExternalIP — variant (4): IP вне кластера. Acceptance TGT-001 / TGR-011:
// допустим публичный IPv4/IPv6; bogon-классы (loopback / link-local / multicast /
// unspecified / broadcast) — запрещены и отлавливаются в Validate.
//
// Private CIDR (RFC1918 10/8, 172.16/12, 192.168/16) — РАЗРЕШЕНЫ (acceptance §6
// design §2.5 — это валидный сценарий «target в чужой VPC по private peering»).
type TargetExternalIP struct {
	Address IPAddress
	ZoneID  option.ValueOf[ZoneID]
}

// Validate — exactly-one-of identity, weight bound, и bogon-check на external_ip.
func (t Target) Validate() error {
	identErr := t.validateIdentityOneOf()
	weightErr := t.Weight.Validate()

	// Per-variant format-validation (address-parse / id-non-empty).
	var variantErr error
	switch {
	case t.IPRef != nil:
		variantErr = t.IPRef.Validate()
	case t.ExternalIP != nil:
		variantErr = t.ExternalIP.Validate()
	}

	return multierr.Combine(identErr, weightErr, variantErr)
}

// validateIdentityOneOf — exactly-one-of проверка. Acceptance TGR-009 verbatim:
// `"target must specify exactly one of: instance_id, nic_id, ip_ref, external_ip"`.
func (t Target) validateIdentityOneOf() error {
	count := 0
	if _, ok := t.InstanceID.Maybe(); ok {
		count++
	}
	if _, ok := t.NicID.Maybe(); ok {
		count++
	}
	if t.IPRef != nil {
		count++
	}
	if t.ExternalIP != nil {
		count++
	}
	if count != 1 {
		return coreerrors.InvalidArgument().
			AddFieldViolation("target",
				"target must specify exactly one of: instance_id, nic_id, ip_ref, external_ip").
			Err()
	}
	return nil
}

// Validate проверяет TargetIPRef — address формат + non-empty subnet_id.
// IP ∈ CIDR проверяется async в worker'е (нужен peer-call).
func (r TargetIPRef) Validate() error {
	idErr := error(nil)
	if r.SubnetID == "" {
		idErr = coreerrors.InvalidArgument().
			AddFieldViolation("target.ip_ref.subnet_id",
				"ip_ref.subnet_id is required").
			Err()
	}
	return multierr.Combine(idErr, r.Address.Validate())
}

// Validate проверяет TargetExternalIP — address формат + bogon-deny.
// Bogon-categories (acceptance TGR-011): loopback / link-local / multicast /
// unspecified / broadcast (255.255.255.255). Private RFC1918 — РАЗРЕШЕНЫ.
func (e TargetExternalIP) Validate() error {
	if err := e.Address.Validate(); err != nil {
		return err
	}
	addr, err := netip.ParseAddr(string(e.Address))
	if err != nil {
		// already filtered by Address.Validate; defensive
		return coreerrors.InvalidArgument().
			AddFieldViolation("target.external_ip", "invalid IP address").
			Err()
	}
	if reason, isBogon := classifyBogon(addr); isBogon {
		return coreerrors.InvalidArgument().
			AddFieldViolation("target.external_ip",
				"external_ip "+addr.String()+" is a bogon ("+reason+") and is not allowed as a target").
			Err()
	}
	return nil
}

// classifyBogon — возвращает описательный reason и true, если address —
// bogon-категория, недопустимая для target.external_ip. Не классифицирует как
// bogon приватные RFC1918 CIDR (10/8, 172.16/12, 192.168/16) — это валидный
// случай «target доступен через private peering» (design §2.5 / acceptance §6).
//
// IPv4-mapped IPv6 рассматриваем как соответствующий IPv4 (Unmap).
func classifyBogon(addr netip.Addr) (reason string, isBogon bool) {
	a := addr.Unmap()
	switch {
	case a.IsUnspecified():
		return "unspecified", true
	case a.IsLoopback():
		return "loopback", true
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return "link-local", true
	case a.IsMulticast():
		return "multicast", true
	}
	// IPv4 broadcast 255.255.255.255 — netip не классифицирует напрямую.
	if a.Is4() && a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return "broadcast", true
	}
	return "", false
}
