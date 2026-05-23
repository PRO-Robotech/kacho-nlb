package loadbalancer

// TODO(KAC-153): UpdateLoadBalancerUseCase с update_mask discipline.
//   - unknown поле в mask → InvalidArgument.
//   - immutable поле (type, region_id, project_id) → InvalidArgument.
//   - empty mask → full-object PATCH с silent-ignore immutable (verbatim-стиль).
