package targetgroup

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// AddTargetsUseCase — добавляет targets в TG (KAC-154, acceptance §6 GWT-TGT-001..010).
//
// Sync (handler-thread):
//   - target_group_id required, len(targets) >= 1;
//   - per-target domain.Target.Validate() (4-way oneof, weight bounds,
//     external_ip bogon-check).
//
// Async worker:
//   - TG.Get + status guard (DELETING → FailedPrecondition; GWT-TGT-010);
//   - per-target peer-validate:
//     - instance: compute.InstanceService.Get + region match (GWT-TGT-001/006);
//     - nic: vpc.NetworkInterfaceService.Get + region match via subnet (GWT-TGT-007);
//     - ip_ref: vpc.SubnetService.Get + region match + IP-in-CIDR (GWT-TGT-004/008);
//     - external_ip: bogon-check уже в Validate (GWT-TGR-011), peer-validate нет;
//   - Writer-TX → AddTargets (ON CONFLICT DO NOTHING per partial UNIQUE) +
//     outbox UPDATED только если >0 строк вставлено (GWT-TGT-002 idempotent
//     no-op) → Commit.
type AddTargetsUseCase struct {
	repo            Repo
	opsRepo         OpsRepo
	instanceClient  InstanceClient
	nicClient       NetworkInterfaceClient
	subnetClient    SubnetClient
	logger          *slog.Logger
}

// NewAddTargetsUseCase конструктор.
func NewAddTargetsUseCase(
	repo Repo, opsRepo OpsRepo,
	inst InstanceClient, nic NetworkInterfaceClient, sub SubnetClient,
	logger *slog.Logger,
) *AddTargetsUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &AddTargetsUseCase{
		repo: repo, opsRepo: opsRepo,
		instanceClient: inst, nicClient: nic, subnetClient: sub,
		logger: logger,
	}
}

// Execute — sync validate + ops insert + spawn worker.
func (u *AddTargetsUseCase) Execute(
	ctx context.Context, req *lbv1.AddTargetsRequest,
) (*operations.Operation, error) {
	tgID := req.GetTargetGroupId()
	if tgID == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if len(req.GetTargets()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one target is required")
	}
	if len(req.GetTargets()) > domain.MaxTargetsPerGroup {
		return nil, status.Errorf(codes.InvalidArgument,
			"too many targets in a single AddTargets call (max %d)", domain.MaxTargetsPerGroup)
	}

	targets := targetsFromPb(req.GetTargets())
	for i := range targets {
		if err := targets[i].Validate(); err != nil {
			return nil, mapDomainErr(err)
		}
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("AddTargets to TargetGroup %s (n=%d)", tgID, len(targets)),
		&lbv1.AddTargetsMetadata{TargetGroupId: tgID},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build operation: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "operation persist: %v", err)
	}
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doAdd(workerCtx, tgID, targets)
	})
	return &op, nil
}

// doAdd — async worker. См. описание use-case'а.
func (u *AddTargetsUseCase) doAdd(ctx context.Context, tgID string, targets []domain.Target) (*anypb.Any, error) {
	// 1. TG.Get + status guard.
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	tg, err := rd.TargetGroups().Get(ctx, tgID)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if tg.Status == domain.TargetGroupStatusDeleting {
		return nil, status.Error(codes.FailedPrecondition, "target group is being deleted")
	}

	// 2. Per-target peer-validate.
	for i := range targets {
		if err := u.validateTargetPeer(ctx, tg.RegionID, i, &targets[i]); err != nil {
			return nil, err
		}
	}

	// 3. Writer-TX → AddTargets + (conditional) outbox UPDATED → Commit.
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	inserted, err := w.TargetGroups().AddTargets(ctx, tgID, targets)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if inserted > 0 {
		if err := w.Outbox().Emit(ctx,
			kachopg.OutboxResourceTargetGroup, tgID, string(tg.ProjectID),
			kachopg.OutboxActionUpdated, map[string]any{
				"id":              tgID,
				"project_id":      string(tg.ProjectID),
				"region_id":       string(tg.RegionID),
				"trigger":         "add_targets",
				"inserted_count":  inserted,
			},
		); err != nil {
			return nil, mapDomainErr(err)
		}
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Re-read TG (с inline targets) для response.
	rd2, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	updated, err := rd2.TargetGroups().Get(ctx, tgID)
	_ = rd2.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return marshalTargetGroup(updated)
}

// validateTargetPeer — per-target peer-validate. idx — индекс target'а в request
// массиве (для verbatim error text `"target[N]..."`). Errors → gRPC InvalidArgument.
func (u *AddTargetsUseCase) validateTargetPeer(
	ctx context.Context, tgRegion domain.RegionID, idx int, t *domain.Target,
) error {
	switch {
	case isInstanceTarget(t):
		return u.validateInstanceTarget(ctx, tgRegion, idx, t)
	case isNicTarget(t):
		return u.validateNicTarget(ctx, tgRegion, idx, t)
	case t.IPRef != nil:
		return u.validateIPRefTarget(ctx, tgRegion, idx, t)
	case t.ExternalIP != nil:
		// bogon-check уже в domain.Target.Validate(); peer-validate нет.
		return nil
	}
	return nil
}

func (u *AddTargetsUseCase) validateInstanceTarget(
	ctx context.Context, tgRegion domain.RegionID, idx int, t *domain.Target,
) error {
	instID, _ := t.InstanceID.Maybe()
	if u.instanceClient == nil {
		return status.Error(codes.Unavailable, "compute instance client not configured")
	}
	inst, err := u.instanceClient.Get(ctx, string(instID))
	if err != nil {
		return mapPeerTargetErr(idx, "instance_id", string(instID), err)
	}
	if r := regionFromZone(inst.ZoneID); r != "" && r != string(tgRegion) {
		return status.Errorf(codes.InvalidArgument,
			"target[%d].instance_id '%s' region '%s' does not match target_group region '%s'",
			idx, instID, r, tgRegion)
	}
	return nil
}

func (u *AddTargetsUseCase) validateNicTarget(
	ctx context.Context, tgRegion domain.RegionID, idx int, t *domain.Target,
) error {
	nicID, _ := t.NicID.Maybe()
	if u.nicClient == nil || u.subnetClient == nil {
		return status.Error(codes.Unavailable, "vpc nic/subnet client not configured")
	}
	nic, err := u.nicClient.Get(ctx, string(nicID))
	if err != nil {
		return mapPeerTargetErr(idx, "nic_id", string(nicID), err)
	}
	// NIC region resolved via parent subnet zone.
	sub, err := u.subnetClient.Get(ctx, nic.SubnetID)
	if err != nil {
		return mapPeerTargetErr(idx, "nic_id", string(nicID), err)
	}
	if r := regionFromZone(sub.ZoneID); r != "" && r != string(tgRegion) {
		return status.Errorf(codes.InvalidArgument,
			"target[%d].nic_id '%s' region '%s' does not match target_group region '%s'",
			idx, nicID, r, tgRegion)
	}
	return nil
}

func (u *AddTargetsUseCase) validateIPRefTarget(
	ctx context.Context, tgRegion domain.RegionID, idx int, t *domain.Target,
) error {
	if u.subnetClient == nil {
		return status.Error(codes.Unavailable, "vpc subnet client not configured")
	}
	subID := string(t.IPRef.SubnetID)
	sub, err := u.subnetClient.Get(ctx, subID)
	if err != nil {
		return mapPeerTargetErr(idx, "ip_ref.subnet_id", subID, err)
	}
	if r := regionFromZone(sub.ZoneID); r != "" && r != string(tgRegion) {
		return status.Errorf(codes.InvalidArgument,
			"target[%d].ip_ref.subnet_id '%s' region '%s' does not match target_group region '%s'",
			idx, subID, r, tgRegion)
	}
	// IP ∈ CIDR check.
	addr, err := netip.ParseAddr(string(t.IPRef.Address))
	if err != nil {
		return status.Errorf(codes.InvalidArgument,
			"target[%d].ip_ref.address %s is not a valid IP", idx, t.IPRef.Address)
	}
	cidrs := sub.V4CIDRBlocks
	if addr.Is6() {
		cidrs = sub.V6CIDRBlocks
	}
	if !addressInAnyCIDR(addr, cidrs) {
		return status.Errorf(codes.InvalidArgument,
			"target[%d].ip_ref.address %s is not in subnet %s CIDR %s",
			idx, t.IPRef.Address, subID, strings.Join(cidrs, ","))
	}
	return nil
}

// mapPeerTargetErr — peer-error → InvalidArgument с verbatim per-target context.
// NotFound от peer → "target[N].<field> '<id>' not found" (acceptance §6 TGT-014).
// Unavailable → Unavailable. Прочие → InvalidArgument.
func mapPeerTargetErr(idx int, field, id string, err error) error {
	if err == nil {
		return nil
	}
	// Sentinel-strip: peer-clients оборачивают `domain.ErrInvalidArg: <Resource> <id> not found`.
	switch {
	case errIsKind(err, domain.ErrNotFound):
		return status.Errorf(codes.InvalidArgument,
			"target[%d].%s '%s' not found", idx, field, id)
	case errIsKind(err, domain.ErrInvalidArg):
		// compute/vpc peer-clients мапят NotFound → InvalidArgument внутри
		// сентинела — это нормальный путь (см. instance_client.mapInstanceErr).
		return status.Errorf(codes.InvalidArgument,
			"target[%d].%s '%s' not found", idx, field, id)
	case errIsKind(err, domain.ErrFailedPrecondition):
		return status.Errorf(codes.FailedPrecondition,
			"target[%d].%s '%s': %v", idx, field, id, err)
	case errIsKind(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable,
			"target[%d].%s '%s': peer lookup unavailable", idx, field, id)
	}
	return status.Errorf(codes.Internal,
		"target[%d].%s '%s': peer lookup failed", idx, field, id)
}

// errIsKind — errors.Is wrapper для удобства switch'а; импортирует errors лениво.
func errIsKind(err error, sentinel error) bool {
	if err == nil || sentinel == nil {
		return false
	}
	return errsIs(err, sentinel)
}

// addressInAnyCIDR — true если addr ∈ хотя бы одного prefix'а из cidrs.
// Невалидный CIDR в slice пропускается без ошибки (тест выше).
func addressInAnyCIDR(addr netip.Addr, cidrs []string) bool {
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// regionFromZone — derive region из zone-id by stripping trailing "-<letter>".
// `ru-central1-a` → `ru-central1`; `ru-central2-b` → `ru-central2`. Если формат
// не подходит — возвращает zone as-is (caller тогда матчит как regional zone).
func regionFromZone(zone string) string {
	if zone == "" {
		return ""
	}
	i := strings.LastIndex(zone, "-")
	if i <= 0 || i == len(zone)-1 {
		return zone
	}
	suffix := zone[i+1:]
	// region-zone у kacho выглядит как "<region>-<letter>" — 1-3 char suffix.
	if len(suffix) >= 1 && len(suffix) <= 3 {
		return zone[:i]
	}
	return zone
}

// isInstanceTarget / isNicTarget — small predicates для switch (option Maybe()
// возвращает (val, ok); хотим только ok).
func isInstanceTarget(t *domain.Target) bool {
	_, ok := t.InstanceID.Maybe()
	return ok
}

func isNicTarget(t *domain.Target) bool {
	_, ok := t.NicID.Maybe()
	return ok
}
