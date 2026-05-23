package config

// TODO(KAC-149): Load(path string) (*Config, error).
//   - viper.New(); v.SetEnvPrefix("KACHO_NLB"); v.SetEnvKeyReplacer("." → "__"); v.AutomaticEnv().
//   - RegisterDefaults(v).
//   - if path != "" → v.SetConfigFile(path); v.ReadInConfig().
//   - var cfg Config; v.Unmarshal(&cfg).
//   - if err := cfg.Validate(); err != nil → error.
