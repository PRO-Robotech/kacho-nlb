// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

// cert_bound_identity_test.go — anti-spoof guard для principal-identity на ОБОИХ
// gRPC-листенерах (9090 public и :9091 internal).
//
// SECURITY: principal — единственный subject per-RPC FGA Check. Прежняя связка
// grpcsrv.UnaryPrincipalExtract / StreamPrincipalExtract БЕЗУСЛОВНО доверяла
// x-kacho-principal-* metadata любого peer'а: peer без верифицированного
// mTLS-client-cert'а мог подделать identity (usr-victim) и получить его права. Fix
// переводит оба листенера на trust-aware связку grpcsrv.UnaryCertIdentityExtract +
// grpcsrv.UnaryTrustedPrincipalExtract (+ опциональный SAN-allowlist форвардеров для
// api-gateway): forwarded principal доходит до use-case'ов/Check только когда
// CertIdentityExtract доказал, что peer mTLS-verified (и, если задан allowlist, что
// его SAN — доверенный форвардер).
//
// Два комплементарных стража:
//  1. wiring guard (source-level): оба листенера используют Trusted-варианты, НЕ
//     legacy; порядок CertIdentityExtract → TrustedPrincipalExtract сохранён.
//  2. behavioral guard: собирает точную цепочку principal-extract и доказывает, что
//     forged principal недоверенного peer'а снимается (carrier остаётся
//     SystemPrincipal, trusted=false), а verified peer — honored.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

const gatewaySAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"

// --- 1. source-level wiring guards ---

// TestListeners_UseTrustAwarePrincipalExtract — оба интерсептор-набора
// (publicUnary/internalUnary + stream-аналоги) обязаны навешивать trust-aware
// CertIdentityExtract + TrustedPrincipalExtract (в этом порядке) и НЕ содержать
// legacy unconditional UnaryPrincipalExtract / StreamPrincipalExtract.
//
// RED-демонстрация: вернуть на любой листенер grpcsrv.UnaryPrincipalExtract —
// тест падает.
func TestListeners_UseTrustAwarePrincipalExtract(t *testing.T) {
	src := readMainSrc(t)

	type listener struct {
		name        string
		marker      string
		certExtract string
		trusted     string
		legacy      string
	}
	for _, l := range []listener{
		{"publicUnary", "publicUnary := []grpc.UnaryServerInterceptor{",
			"grpcsrv.UnaryCertIdentityExtract()", "grpcsrv.UnaryTrustedPrincipalExtract(", "grpcsrv.UnaryPrincipalExtract()"},
		{"internalUnary", "internalUnary := []grpc.UnaryServerInterceptor{",
			"grpcsrv.UnaryCertIdentityExtract()", "grpcsrv.UnaryTrustedPrincipalExtract(", "grpcsrv.UnaryPrincipalExtract()"},
		{"publicStream", "publicStream := []grpc.StreamServerInterceptor{",
			"grpcsrv.StreamCertIdentityExtract()", "grpcsrv.StreamTrustedPrincipalExtract(", "grpcsrv.StreamPrincipalExtract()"},
		{"internalStream", "internalStream := []grpc.StreamServerInterceptor{",
			"grpcsrv.StreamCertIdentityExtract()", "grpcsrv.StreamTrustedPrincipalExtract(", "grpcsrv.StreamPrincipalExtract()"},
	} {
		block := braceBlockAfter(t, src, l.marker)
		if !strings.Contains(block, l.certExtract) {
			t.Errorf("%s: missing %s — principal-metadata НЕ привязана к mTLS-cert'у (spoof risk)", l.name, l.certExtract)
		}
		if !strings.Contains(block, l.trusted) {
			t.Errorf("%s: missing %s — principal-metadata НЕ trust-gated", l.name, l.trusted)
		}
		if strings.Contains(block, l.legacy) {
			t.Errorf("%s: still wires legacy %s — peer без verified client-cert может подделать principal'а", l.name, l.legacy)
		}
		// Порядок: CertIdentityExtract обязан стоять раньше TrustedPrincipalExtract
		// (Trusted читает verified-флаг, который ставит cert-extract).
		ci := strings.Index(block, l.certExtract)
		tp := strings.Index(block, l.trusted)
		if ci < 0 || tp < 0 || ci >= tp {
			t.Errorf("%s: ordering violated — %s обязан идти раньше %s", l.name, l.certExtract, l.trusted)
		}
	}
}

// --- 2. behavioral guards ---

// TestPrincipalChain_DropsForgedPrincipal_HonorsVerified — точная цепочка
// principal-extract (CertIdentityExtract → TrustedPrincipalExtract), как на обоих
// листенерах. Без SAN-allowlist (пусто → доверяем любому verified peer'у —
// insecure dev/back-compat).
//
//   - unverified TLS peer с forged x-kacho-principal-* → principal снимается,
//     carrier остаётся SystemPrincipal (RED с legacy extractor'ом — он бы
//     проштамповал usr-mallory как subject Check'а).
//   - mTLS-verified peer → principal honored (без регресса для verified-вызовов).
func TestPrincipalChain_DropsForgedPrincipal_HonorsVerified(t *testing.T) {
	chain := principalChainUnderTest()

	t.Run("unverified_tls_peer_forged_principal_dropped", func(t *testing.T) {
		ctx := withForgedPrincipal(unverifiedTLSPeerCtx(), "usr-mallory")

		carrierID, trusted := runChain(t, chain, ctx)
		if trusted {
			t.Errorf("principal недоверенного TLS-peer'а НЕ должен быть trusted")
		}
		if carrierID != operations.SystemPrincipal().ID {
			t.Errorf("forged principal протёк в carrier: got %q, want system fallback %q", carrierID, operations.SystemPrincipal().ID)
		}
		if carrierID == "usr-mallory" {
			t.Errorf("spoof: forged principal id 'usr-mallory' дошёл до subject'а FGA Check")
		}
	})

	t.Run("verified_mtls_peer_principal_honored", func(t *testing.T) {
		ctx := withForgedPrincipal(verifiedPeerCtx(t, gatewaySAN), "usr-alice")

		carrierID, trusted := runChain(t, chain, ctx)
		if !trusted {
			t.Errorf("principal verified mTLS-peer'а обязан быть trusted (без регресса)")
		}
		if carrierID != "usr-alice" {
			t.Errorf("verified principal не honored: got %q, want %q", carrierID, "usr-alice")
		}
	})
}

// TestPrincipalChain_ForwarderAllowlist_DropsNonGateway — с заданным SAN-allowlist
// (api-gateway SA) end-user principal форвардится ТОЛЬКО когда cert-identity peer'а
// ∈ allowlist. Verified-но-не-форвардер (внутренний сервис со своим валидным
// cert'ом) подделать пользователя НЕ может (anti-confused-deputy).
func TestPrincipalChain_ForwarderAllowlist_DropsNonGateway(t *testing.T) {
	chain := principalChainUnderTest(gatewaySAN)

	t.Run("verified_non_gateway_peer_principal_dropped", func(t *testing.T) {
		other := "spiffe://kacho.cloud/ns/kacho/sa/kacho-vpc"
		ctx := withForgedPrincipal(verifiedPeerCtx(t, other), "usr-victim")

		carrierID, trusted := runChain(t, chain, ctx)
		if trusted {
			t.Errorf("verified-но-не-форвардер peer (%s) НЕ должен форвардить end-user principal'а", other)
		}
		if carrierID == "usr-victim" {
			t.Errorf("confused-deputy: internal-сервис проштамповал чужого principal'а 'usr-victim'")
		}
	})

	t.Run("gateway_peer_principal_honored", func(t *testing.T) {
		ctx := withForgedPrincipal(verifiedPeerCtx(t, gatewaySAN), "usr-alice")

		carrierID, trusted := runChain(t, chain, ctx)
		if !trusted {
			t.Errorf("principal от доверенного форвардера (api-gateway SAN) обязан быть honored")
		}
		if carrierID != "usr-alice" {
			t.Errorf("gateway-forwarded principal не honored: got %q, want %q", carrierID, "usr-alice")
		}
	})
}

// --- helpers ---

// principalChainUnderTest собирает ту же unary-цепочку principal-extract, что
// навешивает main.go на оба листенера. forwarderSANs пробрасываются как
// WithTrustedForwarders (пусто → trust любого verified peer'а).
func principalChainUnderTest(forwarderSANs ...string) grpc.UnaryServerInterceptor {
	return chainUnaryServer(
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarderSANs...)),
	)
}

func runChain(t *testing.T, chain grpc.UnaryServerInterceptor, ctx context.Context) (carrierID string, trusted bool) {
	t.Helper()
	final := func(c context.Context, _ any) (any, error) {
		carrierID = operations.PrincipalFromContext(c).ID
		_, trusted = grpcsrv.TrustedPrincipalFromContext(c)
		return nil, nil
	}
	if _, err := chain(ctx, nil, nil, final); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	return carrierID, trusted
}

func withForgedPrincipal(ctx context.Context, id string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, id,
		grpcsrv.MDKeyPrincipalDisplay, id+"@example.com",
	))
}

// unverifiedTLSPeerCtx — TLS present, но НЕТ verified client-cert (пустой
// VerifiedChains) — ровно то, как выглядит cert-less/unverified peer.
func unverifiedTLSPeerCtx() context.Context {
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}}}
	return peer.NewContext(context.Background(), tlsPeer)
}

// verifiedPeerCtx — mTLS-verified peer: непустая verified-chain с leaf-cert'ом,
// несущим переданный SPIFFE-SAN.
func verifiedPeerCtx(t *testing.T, san string) context.Context {
	t.Helper()
	leaf := &x509.Certificate{URIs: mustParseURIs(t, san)}
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}}
	return peer.NewContext(context.Background(), tlsPeer)
}

// chainUnaryServer композирует unary server-интерсепторы слева-направо вокруг
// финального handler'а (семантика grpc.ChainUnaryInterceptor).
func chainUnaryServer(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		chained := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := chained
			chained = func(c context.Context, r any) (any, error) { return ic(c, r, info, next) }
		}
		return chained(ctx, req)
	}
}

func readMainSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	return string(b)
}

// braceBlockAfter возвращает текст {... }-блока, начинающегося с открывающей
// фигурной скобки в marker, балансируя скобки. Используется для среза
// интерсептор-слайсов publicUnary/internalUnary/… из main.go.
func braceBlockAfter(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("main.go: marker %q не найден", marker)
	}
	open := strings.LastIndexByte(src[:i+len(marker)], '{')
	depth := 0
	for j := open; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : j+1]
			}
		}
	}
	t.Fatalf("main.go: несбалансированные скобки после marker %q", marker)
	return ""
}

func mustParseURIs(t *testing.T, raw ...string) []*url.URL {
	t.Helper()
	out := make([]*url.URL, 0, len(raw))
	for _, r := range raw {
		u, err := url.Parse(r)
		if err != nil {
			t.Fatalf("parse uri %q: %v", r, err)
		}
		out = append(out, u)
	}
	return out
}
