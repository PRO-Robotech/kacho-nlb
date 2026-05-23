package targetgroup

// TODO(KAC-164): AddTargetsUseCase — idempotent INSERT ... ON CONFLICT DO NOTHING
// на partial UNIQUE NULLS NOT DISTINCT по 4-way identity (instance_id|nic_id|ip_ref|external_ip).
//   - cross-service validation: instance_id/nic_id → compute|vpc Get; ip_ref → subnet Get + ip in range; external_ip → IP-format only.
