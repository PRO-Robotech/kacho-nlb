package listener

// TODO(KAC-160): DeleteListenerUseCase.
//   - release VIP (vpc.AddressService.ClearReference) если auto-allocated.
//   - DELETE listener; FK CASCADE чистит default_target_group_id refs.
