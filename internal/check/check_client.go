package check

// TODO(KAC-152): gRPC interceptor wiring через kacho-corelib/authz.Check.
//
//   - конструктор NewCheckClient(iamConn, cache TTL) → returns grpc.UnaryServerInterceptor.
//   - interceptor: extract subject из ctx (corelib/authz.SubjectExtract) → lookup
//     permission из PermissionMap() → corelib/authz.Check (FGA Check) → allow|deny.
//   - cache decisions (TTL 5s default) — корелибный rate-limit + cache.
