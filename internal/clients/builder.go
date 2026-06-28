// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package clients — cross-service gRPC client builder для kacho-nlb.
//
// Единая точка сборки gRPC-клиентских соединений к peer-сервисам согласно
// «`dial<Peer>` заменить на
// `H-BF/corlib/client/grpc/client-builder.go` — единый паттерн для всех
// gRPC-клиентов (retries, LB, TLS, metrics)».
//
// Builder — обёртка над corlib `ClientFromAddress` с дефолтами kacho-nlb
// (retries=3, dialTimeout=10s, KeepAlive 30s, userAgent="kacho-nlb").
// Pattern скопирован с kacho-vpc/internal/clients/builder.go.
//
// Этот файл — building block. Конкретные peer-клиенты живут в подпакетах
// `internal/clients/{iam,compute,vpc}` — тонкие обёртки поверх Build,
// реализующие port-интерфейсы из соответствующих use-case-пакетов
// (Clean Architecture: адаптеры реализуют порты use-case-слоя).
package clients

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"strings"
	"time"

	corlibgrpc "github.com/H-BF/corlib/client/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Conn — то, что нужно generated proto-клиентам (`grpc.ClientConnInterface`)
// плюс возможность Close. Подходит и corlib `ClientConn`, и `*grpc.ClientConn`.
type Conn interface {
	grpc.ClientConnInterface
	io.Closer
}

// BuildOptions — параметры сборки cross-service gRPC-клиента.
type BuildOptions struct {
	Endpoint      string        // host:port
	TLS           bool          // true → TLS 1.2+; false → insecure (dev)
	Retries       uint          // gRPC retries on Unavailable (default 3)
	DialTimeout   time.Duration // dial backoff target (default 10s)
	KeepAliveTime time.Duration // ping every (default 30s)
	UserAgent     string        // gRPC User-Agent (default "kacho-nlb")

	// MTLSCreds — opt-in per-edge mTLS transport-credentials (built from
	// the corelib grpcclient.TLSClient via clients.MTLSCredsFor). When non-nil it
	// OVERRIDES the simple TLS bool above: the dial presents a client-cert and
	// verifies the server-cert against the configured CA + server_name.
	// nil → fall back to buildCreds(opts.TLS) (legacy system-trust TLS / insecure).
	MTLSCreds credentials.TransportCredentials
}

const (
	defaultRetries       = 3
	defaultDialTimeout   = 10 * time.Second
	defaultKeepAliveTime = 30 * time.Second
	defaultUserAgent     = "kacho-nlb"
)

func (o BuildOptions) withDefaults() BuildOptions {
	if o.Retries == 0 {
		o.Retries = defaultRetries
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = defaultDialTimeout
	}
	if o.KeepAliveTime == 0 {
		o.KeepAliveTime = defaultKeepAliveTime
	}
	if o.UserAgent == "" {
		o.UserAgent = defaultUserAgent
	}
	return o
}

// Build открывает gRPC-клиентское соединение через corlib client-builder
// (единый паттерн). Возвращает Conn (`grpc.ClientConnInterface +
// io.Closer`), который принимают generated proto-клиенты.
func Build(ctx context.Context, opts BuildOptions) (Conn, error) {
	if strings.TrimSpace(opts.Endpoint) == "" {
		return nil, fmt.Errorf("clients.Build: empty Endpoint")
	}
	opts = opts.withDefaults()

	// per-edge mTLS creds override the legacy TLS bool when provided.
	creds := opts.MTLSCreds
	if creds == nil {
		creds = buildCreds(opts.TLS)
	}

	cc, err := corlibgrpc.ClientFromAddress(opts.Endpoint).
		WithCreds(creds).
		WithDialDuration(opts.DialTimeout).
		WithMaxRetries(opts.Retries).
		WithUserAgent(opts.UserAgent).
		WithKeepAlive(keepalive.ClientParameters{
			Time:                opts.KeepAliveTime,
			Timeout:             opts.KeepAliveTime / 3, // ack within 1/3 of ping interval
			PermitWithoutStream: false,
		}).
		New(ctx)
	if err != nil {
		return nil, fmt.Errorf("clients.Build: corlib dial %q: %w", opts.Endpoint, err)
	}
	return cc, nil
}

// buildCreds — единый source-of-truth TLS / insecure для всех cross-service
// клиентов; TLS MinVersion=1.2 верифицирует server-сертификат по системному
// trust store (production-strict mode требует TLS, см. config.ModeProduction).
func buildCreds(useTLS bool) credentials.TransportCredentials {
	if useTLS {
		return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	return insecure.NewCredentials()
}
