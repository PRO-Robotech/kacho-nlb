package config

import "github.com/spf13/viper"

// RegisterDefaults — единственное место, где задаются дефолты конфига
// (evgeniy §8.J.3: defaults не в struct-tags, не в main, а в config-пакете).
//
// TODO(KAC-149): полный набор дефолтов согласно spec §6.9 acceptance-доку.
func RegisterDefaults(v *viper.Viper) {
	v.SetDefault("logger.level", "DEBUG")
	v.SetDefault("api-server.endpoint", "tcp://0.0.0.0:9090")
	v.SetDefault("api-server.internal-endpoint", "tcp://0.0.0.0:9091")
	v.SetDefault("api-server.graceful-shutdown", "10s")
	v.SetDefault("api-server.grpc-gw-enable", false)
	v.SetDefault("metrics.enable", true)
	v.SetDefault("healthcheck.enable", true)
	v.SetDefault("repository.type", "POSTGRES")
	v.SetDefault("authz.iam.dial-deadline", "3s")
	v.SetDefault("authz.iam.request-timeout", "500ms")
	v.SetDefault("extapi.def-dial-duration", "10s")
}
