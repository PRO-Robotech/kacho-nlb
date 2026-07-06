// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// cmd/migrator/main_test.go —; покрывает только парсинг cobra-флагов
// + резолвинг диалекта/DSN. Реальный apply миграций — integration-тесты в
// internal/repo/...integration_test.go (testcontainers Postgres + goose),
// которые появятся в. Тут — быстрые unit-тесты без БД и без Docker.
package main

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func emptyFS() fs.FS { return fstest.MapFS{} }

// runCommand — helper: парсит args, ловит ошибки cobra. Stdout/stderr
// захватывается для проверок.
func runCommand(t *testing.T, args []string, env map[string]string) (stdout, stderr string, err error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	cmd := newRootCmd(emptyFS())
	var sout, serr bytes.Buffer
	cmd.SetOut(&sout)
	cmd.SetErr(&serr)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return sout.String(), serr.String(), err
}

func TestRootCmd_HelpDoesNotError(t *testing.T) {
	stdout, _, err := runCommand(t, []string{"--help"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "kacho-nlb-migrator") {
		t.Fatalf("expected help to mention kacho-nlb-migrator, got: %q", stdout)
	}
	for _, sub := range []string{"up", "down", "status", "create"} {
		if !strings.Contains(stdout, sub) {
			t.Errorf("help missing subcommand %q", sub)
		}
	}
}

func TestUpCmd_UnknownDialectFails(t *testing.T) {
	_, _, err := runCommand(t, []string{
		"--dialect", "bogus-dialect",
		"--dsn", "postgres://x:y@z:1/d?sslmode=disable",
		"up", "--target", "10",
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown dialect, got nil")
	}
	if !strings.Contains(err.Error(), "unknown dialect") {
		t.Fatalf("expected 'unknown dialect' error, got: %v", err)
	}
}

func TestDownCmd_ParsesTargetFlag(t *testing.T) {
	_, _, err := runCommand(t, []string{
		"--dialect", "bogus",
		"--dsn", "postgres://x:y@z:1/d?sslmode=disable",
		"down", "--target", "5",
	}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown dialect") {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestCreateCmd_RequiresNameArg(t *testing.T) {
	_, _, err := runCommand(t, []string{
		"--dsn", "postgres://x:y@z:1/d?sslmode=disable",
		"create",
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing name arg, got nil")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Fatalf("expected cobra Args error, got: %v", err)
	}
}

func TestBuildRunner_DSNFromFlag(t *testing.T) {
	opts := &rootOptions{dialect: "postgres", dsn: "postgres://u:p@h:5432/db?sslmode=disable"}
	r, err := buildRunner(opts, fstest.MapFS{"0001_x.sql": &fstest.MapFile{Data: []byte("-- empty")}})
	if err != nil {
		t.Fatalf("buildRunner: %v", err)
	}
	if r == nil {
		t.Fatal("nil runner")
	}
}

func TestBuildRunner_EnvDSNFallback(t *testing.T) {
	// mode defaults to fail-closed production; the migrator only needs the DSN,
	// so opt into dev explicitly to exercise the env-DSN fallback path.
	t.Setenv("KACHO_NLB_MODE", "dev")
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://envuser:envpass@h/db")
	opts := &rootOptions{dialect: "postgres" /* dsn пуст*/}
	r, err := buildRunner(opts, fstest.MapFS{"0001_x.sql": &fstest.MapFile{Data: []byte("-- empty")}})
	if err != nil {
		t.Fatalf("buildRunner: %v", err)
	}
	if r == nil {
		t.Fatal("nil runner")
	}
}

func TestBuildRunner_NoDSN_NoConfig_Fails(t *testing.T) {
	opts := &rootOptions{dialect: "postgres"}
	_, err := buildRunner(opts, emptyFS())
	if err == nil {
		t.Fatal("expected error when DSN/ENV/config all empty, got nil")
	}
	// config.Load выкидывает validation-ошибку про repository.postgres.url
	if !strings.Contains(err.Error(), "repository.postgres.url") &&
		!strings.Contains(err.Error(), "dsn unset") {
		t.Fatalf("expected DSN-source error, got: %v", err)
	}
}
