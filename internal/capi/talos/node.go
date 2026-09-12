// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/constants"
)

// Node is the Talos API surface the reconciler needs.
type Node interface {
	// Probe classifies what answers on the Talos API port. tc may be nil
	// before secrets exist; a configured node is then reported Foreign.
	Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error)
	// ApplyConfiguration applies a machine config to a node in maintenance
	// mode with reboot mode.
	ApplyConfiguration(ctx context.Context, cfg []byte) error
	// Bootstrap bootstraps etcd on a configured node.
	Bootstrap(ctx context.Context, tc *clientconfig.Config) error
	// EtcdState reports whether etcd has members on a configured node.
	EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error)
	// Kubeconfig fetches the admin kubeconfig from a configured node.
	Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error)
	// Reset wipes the node's system disk without leaving etcd and halts it.
	Reset(ctx context.Context, tc *clientconfig.Config) error
}

// MachineryNode implements Node with the Talos machinery client.
type MachineryNode struct {
	// endpoint is "ip" or "ip:port"; the client appends port 50000 when missing.
	endpoint    string
	dialTimeout time.Duration
}

// NewMachineryNode returns a Node for the Talos API at ip (port 50000).
func NewMachineryNode(ip string) *MachineryNode {
	return &MachineryNode{endpoint: ip, dialTimeout: 10 * time.Second}
}

// insecureClient dials without verifying the server certificate and records
// the server certificate's common name into cn.
func (n *MachineryNode) insecureClient(ctx context.Context, cn *string) (*client.Client, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // maintenance mode has no CA; identity is checked via CN
		MinVersion:         tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				*cn = cs.PeerCertificates[0].Subject.CommonName
			}
			return nil
		},
	}
	return client.New(ctx, client.WithEndpoints(n.endpoint), client.WithTLSConfig(tlsCfg))
}

// authClient dials with this run's talosconfig, overriding endpoints to the node.
func (n *MachineryNode) authClient(ctx context.Context, tc *clientconfig.Config) (*client.Client, error) {
	return client.New(ctx, client.WithConfig(tc), client.WithEndpoints(n.endpoint))
}

// Probe implements Node.
func (n *MachineryNode) Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error) {
	ctx, cancel := context.WithTimeout(ctx, n.dialTimeout)
	defer cancel()

	var serverCN string
	c, err := n.insecureClient(ctx, &serverCN)
	if err != nil {
		return TalosUnreachable, nil
	}
	_, verr := c.Version(ctx)
	_ = c.Close()

	// No certificate seen means no TLS handshake completed: nothing is listening.
	if serverCN == "" {
		return TalosUnreachable, nil
	}
	if verr == nil && serverCN == constants.MaintenanceServiceCommonName {
		return TalosMaintenance, nil
	}
	if serverCN == constants.MaintenanceServiceCommonName {
		// Maintenance service answered TLS but the RPC failed; try again later.
		return TalosUnreachable, nil
	}

	// A configured node presented a certificate. Is it ours?
	if tc == nil {
		return TalosForeign, nil
	}
	ac, err := n.authClient(ctx, tc)
	if err != nil {
		return TalosForeign, nil
	}
	defer func() { _ = ac.Close() }()
	if _, err := ac.Version(ctx); err != nil {
		return TalosForeign, nil
	}
	return TalosOurs, nil
}

// ApplyConfiguration implements Node.
func (n *MachineryNode) ApplyConfiguration(ctx context.Context, cfg []byte) error {
	var cn string
	c, err := n.insecureClient(ctx, &cn)
	if err != nil {
		return fmt.Errorf("dialing maintenance service: %w", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: cfg,
		Mode: machineapi.ApplyConfigurationRequest_REBOOT,
	})
	if err != nil {
		return fmt.Errorf("applying configuration: %w", err)
	}
	return nil
}

// Bootstrap implements Node.
func (n *MachineryNode) Bootstrap(ctx context.Context, tc *clientconfig.Config) error {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Bootstrap(ctx, &machineapi.BootstrapRequest{})
}

// EtcdState implements Node. Any error listing members is reported as not
// bootstrapped, because etcd is not serving.
func (n *MachineryNode) EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error) {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return EtcdUnknown, err
	}
	defer func() { _ = c.Close() }()

	resp, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
	if err != nil {
		return EtcdNotBootstrapped, nil
	}
	for _, msg := range resp.GetMessages() {
		if len(msg.GetMembers()) > 0 {
			return EtcdBootstrapped, nil
		}
	}
	return EtcdNotBootstrapped, nil
}

// Kubeconfig implements Node.
func (n *MachineryNode) Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error) {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Kubeconfig(ctx)
}

// Reset implements Node.
func (n *MachineryNode) Reset(ctx context.Context, tc *clientconfig.Config) error {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.ResetGeneric(ctx, &machineapi.ResetRequest{Graceful: false, Reboot: false})
}
