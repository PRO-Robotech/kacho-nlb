package config

// TODO(KAC-149): Validate() error — проверяет required-поля через multierr.Combine.
//   - APIServer.Endpoint / InternalEndpoint непустые.
//   - Repository.Postgres.URL непустой.
//   - Authz.IAM.Addr непустой.
//   - Logger.Level ∈ {FATAL,ERROR,WARN,INFO,DEBUG}.
//   - Любое неизвестное Repository.Type → error.
