// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeMachineService struct {
	machineapi.UnimplementedMachineServiceServer
}

func (fakeMachineService) Version(context.Context, *emptypb.Empty) (*machineapi.VersionResponse, error) {
	return &machineapi.VersionResponse{Messages: []*machineapi.Version{{Version: &machineapi.VersionInfo{Tag: "v1.13.6"}}}}, nil
}

func selfSignedCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTalosLike starts a gRPC MachineService on 127.0.0.1 with the given
// server cert and client-auth policy and returns "host:port".
func startTalosLike(t *testing.T, cert tls.Certificate, clientAuth tls.ClientAuthType) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   clientAuth,
		MinVersion:   tls.VersionTLS12,
	})))
	machineapi.RegisterMachineServiceServer(srv, fakeMachineService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestMachineryNode_ProbeUnreachable(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // nothing listens here now

	n := NewMachineryNode(addr)
	n.dialTimeout = 500 * time.Millisecond
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosUnreachable {
		t.Fatalf("Probe() = %q, want unreachable", got)
	}
}

func TestMachineryNode_ProbeMaintenance(t *testing.T) {
	addr := startTalosLike(t, selfSignedCert(t, constants.MaintenanceServiceCommonName), tls.NoClientCert)
	n := NewMachineryNode(addr)
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosMaintenance {
		t.Fatalf("Probe() = %q, want maintenance", got)
	}
}

func TestMachineryNode_ProbeConfiguredWithoutTalosconfigIsForeign(t *testing.T) {
	addr := startTalosLike(t, selfSignedCert(t, "apid"), tls.RequireAnyClientCert)
	n := NewMachineryNode(addr)
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosForeign {
		t.Fatalf("Probe() = %q, want foreign", got)
	}
}

func TestIsMaintenanceTeardownErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable with cert-required", status.Error(codes.Unavailable, `connection error: desc = "error reading server preface: remote error: tls: certificate required"`), true},
		{"plain unavailable", status.Error(codes.Unavailable, "connection refused"), true},
		{"io.EOF", io.EOF, true},
		{"transport closing", fmt.Errorf("rpc error: %s", "transport is closing"), true},
		{"invalid config rejected", status.Error(codes.InvalidArgument, "invalid machine configuration"), false},
		{"internal error", status.Error(codes.Internal, "boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMaintenanceTeardownErr(context.Background(), tt.err); got != tt.want {
				t.Errorf("isMaintenanceTeardownErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsMaintenanceTeardownErr_CancelledContextIsNotTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Even an otherwise-teardown-looking error is a real abort once the caller
	// cancelled the context.
	if isMaintenanceTeardownErr(ctx, status.Error(codes.Unavailable, "connection refused")) {
		t.Error("cancelled context should not be treated as maintenance teardown")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want Canceled", ctx.Err())
	}
}
