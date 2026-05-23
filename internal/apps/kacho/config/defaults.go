// defaults.go — единственное место, где задаются дефолты config'а
// (evgeniy §8.J.3: defaults не в struct-tags, не в main, а в config-пакете).
//
// Все ключи — viper-paths с делимитером `.` (binding в `KACHO_NLB_…__…`
// через `SetEnvKeyReplacer(".", "__")` в `load.go`).
package config

import "github.com/spf13/viper"

// RegisterDefaults биндит дефолты для всех опциональных полей. Required-поля
// (`repository.postgres.url`, `authz.iam.addr` в production-mode) НЕ
// дефолтятся — их отсутствие ловит `Config.Validate()`.
func RegisterDefaults(v *viper.Viper) {
	// Mode: dev — безопасный default. Production-deploy явно ставит
	// `mode: production` в ConfigMap.
	v.SetDefault("mode", "dev")

	// Logger
	v.SetDefault("logger.level", "DEBUG")

	// API-server
	v.SetDefault("api-server.endpoint", "tcp://0.0.0.0:9090")
	v.SetDefault("api-server.internal-endpoint", "tcp://0.0.0.0:9091")
	v.SetDefault("api-server.graceful-shutdown", "10s")
	v.SetDefault("api-server.grpc-gw-enable", false)

	// Metrics / Healthcheck
	v.SetDefault("metrics.enable", true)
	v.SetDefault("healthcheck.enable", true)

	// Repository
	v.SetDefault("repository.type", "POSTGRES")
	v.SetDefault("repository.postgres.max-conns", int32(0))
	v.SetDefault("repository.postgres.conn-lifetime", "0s")
	// Empty defaults для required-полей: нужны, чтобы viper.AutomaticEnv
	// нашёл соответствующие ENV ключи (`KACHO_NLB_REPOSITORY__POSTGRES__URL`).
	// Validate ловит пустые после Unmarshal.
	v.SetDefault("repository.postgres.url", "")
	v.SetDefault("repository.postgres.slave-url", "")

	// Authn (transport TLS)
	v.SetDefault("authn.type", "none")
	v.SetDefault("authn.tls.client.verify", "skip")

	// ExtAPI (peer gRPC clients)
	v.SetDefault("extapi.def-dial-duration", "10s")
	v.SetDefault("extapi.vpc.dial-duration", "0s")     // 0 → берёт def-dial-duration
	v.SetDefault("extapi.compute.dial-duration", "0s")
	v.SetDefault("extapi.iam.dial-duration", "0s")

	// Authz (FGA Check + cache + listen-invalidator)
	v.SetDefault("authz.iam.addr", "") // empty → AutomaticEnv binds KACHO_NLB_AUTHZ__IAM__ADDR
	v.SetDefault("authz.iam.dial-deadline", "3s")
	v.SetDefault("authz.iam.request-timeout", "500ms")
	v.SetDefault("authz.cache.enable", true)
	v.SetDefault("authz.cache.ttl", "5s")  // NFR KAC-108: ≤10s
	v.SetDefault("authz.cache.size", 10000)
	v.SetDefault("authz.listen-invalidator.enable", false)
	v.SetDefault("authz.listen-invalidator.channel", "kacho_iam_subjects")
	v.SetDefault("authz.breakglass", false)

	// FGA tuple-write
	v.SetDefault("fga.tuple-write.timeout", "2s")
	v.SetDefault("fga.tuple-write.max-retries", 3)

	// Jobs (background workers)
	// target-drain: Phase B 2-phase drain runner (KAC-159). 10s — компромисс
	// между latency удаления expired targets и нагрузкой на БД.
	v.SetDefault("jobs.target-drain.interval", "10s")
}
