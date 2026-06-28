// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// load.go — viper loader.
//
// Flow:
//
//  1. v := viper.New
//  2. v.SetEnvPrefix("KACHO_NLB"); v.SetEnvKeyReplacer(".", "__"); AutomaticEnv
//  3. RegisterDefaults(v)
//  4. if path != "" → v.SetConfigFile(path); ReadInConfig
//  5. var cfg Config; v.Unmarshal(&cfg)
//  6. cfg.Validate — multierr-combined required-checks
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// envPrefix используется в `viper.SetEnvPrefix`. ENV variable полное имя —
// `KACHO_NLB_<UPPER_SNAKE_VIPER_PATH>` с делимитером `__` (см. envKeyDelimiter).
const envPrefix = "KACHO_NLB"

// envKeyDelimiter — разделитель vp-path сегментов в ENV (viper делит конфиг
// на nested-keys через `.`; в ENV это превращается в `__`). Так
// `repository.postgres.url` → `KACHO_NLB_REPOSITORY__POSTGRES__URL`.
const envKeyDelimiter = "__"

// Load читает YAML config (если path != "") + ENV overrides, заполняет
// defaults и валидирует.
//
// path == "" — config-файл не используется (только ENV + defaults). Полезно
// для тестов и unit-stages, где нет ConfigMap.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", envKeyDelimiter))
	v.AutomaticEnv()

	RegisterDefaults(v)

	path = strings.TrimSpace(path)
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			// Несуществующий файл — strict error: если path указан явно, он
			// обязан существовать (config-misconfig поймается на старте, а не
			// silently через default'ы).
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("config file %q does not exist: %w", path, err)
			}
			return nil, fmt.Errorf("read config %q: %w", path, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	expandPasswordFromEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

// expandPasswordFromEnv —. Helm рендерит `postgres.url` с
// shell-style placeholder `$(KACHO_NLB_DB_PASSWORD)` (password хранится в
// Secret, не в ConfigMap). Viper не expand'ит `$(VAR)` синтаксис, поэтому
// без явной подстановки migrator/api-server передают literal placeholder в
// pgx → connection fail → init-container CrashLoopBackOff.
//
// Подставляет значение env-переменной `cfg.Repository.Postgres.PasswordFromEnv`
// вместо `$(<имя>)` в URL и SlaveURL. Env-var не задана → placeholder
// остаётся (failure surface на connect, не silent «постгрес с пустым
// паролем»). `password-from-env: ""` → ничего не делаем.
func expandPasswordFromEnv(cfg *Config) {
	envName := strings.TrimSpace(cfg.Repository.Postgres.PasswordFromEnv)
	if envName == "" {
		return
	}
	pass := os.Getenv(envName)
	if pass == "" {
		return
	}
	placeholder := "$(" + envName + ")"
	cfg.Repository.Postgres.URL = strings.ReplaceAll(cfg.Repository.Postgres.URL, placeholder, pass)
	cfg.Repository.Postgres.SlaveURL = strings.ReplaceAll(cfg.Repository.Postgres.SlaveURL, placeholder, pass)
}
