package listener

// TODO(KAC-158): CreateListenerUseCase.
//   - VIP allocation: вызов vpc.InternalAddressService.AllocateInternalIP (auto)
//     ИЛИ BYO existing vpc.Address (sync precheck + SetReference CAS с
//     used_by=nlb_listener:<id>).
//   - INTERNAL listener требует subnet_id; sync vpc.SubnetService.Get.
//   - same-region constraint listener.region_id == LB.region_id (DB CHECK).
//   - DB UNIQUE (region_id, allocated_address, port, protocol) WHERE status!='DELETING'.
