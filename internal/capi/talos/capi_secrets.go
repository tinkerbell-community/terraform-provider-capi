// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	stdx509 "crypto/x509"
	"fmt"
	"time"

	cryptox509 "github.com/siderolabs/crypto/x509"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigsyaml "sigs.k8s.io/yaml"
)

// adminKubeconfigValidity is how long the derived admin client certificate is
// valid. It is minted from the cluster's stored K8s CA, so it can be reissued at
// any time from the same secrets bundle.
const adminKubeconfigValidity = 10 * 365 * 24 * time.Hour

const (
	// clusterNameLabel is CAPI's cluster.x-k8s.io/cluster-name label, set on the
	// secrets so they follow CAPI convention and are garbage-collected with the
	// cluster. It is not required for adoption.
	clusterNameLabel = "cluster.x-k8s.io/cluster-name"
	// talosBundleSecretKey is the data key the Talos bootstrap provider (CABPT)
	// reads the machine-secrets bundle from in the <cluster>-talos Secret.
	talosBundleSecretKey = "bundle"
	// kubeconfigSecretKey is the data key CAPI reads a cluster kubeconfig from in
	// the <cluster>-kubeconfig Secret.
	kubeconfigSecretKey = "value"
	// kubeconfigSecretType is the Secret type CAPI uses for cluster kubeconfigs.
	kubeconfigSecretType = "cluster.x-k8s.io/secret"
)

// talosSecretsBundleManifest renders the Talos machine-secrets Secret
// (<cluster>-talos, single data key "bundle") that CABPT adopts by
// name+namespace instead of generating fresh secrets, so the CAPI-built cluster
// shares the bootstrap node's PKI. bundleYAML is the yaml-marshaled
// secrets.Bundle (as CABPT unmarshals it).
func talosSecretsBundleManifest(clusterName, namespace, bundleYAML string) ([]byte, error) {
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-talos",
			Namespace: namespace,
			Labels:    map[string]string{clusterNameLabel: clusterName},
		},
		Data: map[string][]byte{talosBundleSecretKey: []byte(bundleYAML)},
	}
	out, err := sigsyaml.Marshal(sec)
	if err != nil {
		return nil, fmt.Errorf("rendering talos machine-secrets Secret: %w", err)
	}
	return out, nil
}

// clusterKubeconfigManifest renders the CAPI cluster kubeconfig Secret
// (<cluster>-kubeconfig, data key "value", type cluster.x-k8s.io/secret) from
// the bootstrap node's kubeconfig, so CAPI and clusterctl talk to the bootstrap
// node's cluster directly rather than waiting for the control-plane provider to
// generate one.
func clusterKubeconfigManifest(clusterName, namespace string, kubeconfig []byte) ([]byte, error) {
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-kubeconfig",
			Namespace: namespace,
			Labels:    map[string]string{clusterNameLabel: clusterName},
		},
		Type: corev1.SecretType(kubeconfigSecretType),
		Data: map[string][]byte{kubeconfigSecretKey: kubeconfig},
	}
	out, err := sigsyaml.Marshal(sec)
	if err != nil {
		return nil, fmt.Errorf("rendering cluster kubeconfig Secret: %w", err)
	}
	return out, nil
}

// deriveKubeconfigFromBundle builds an admin kubeconfig purely from the cluster's
// stored secrets bundle — no running cluster required. It mints an admin client
// certificate (CN=admin, O=system:masters) from the bundle's Kubernetes CA and
// assembles a kubeconfig pointing at endpoint, mirroring the Talos provider's
// static cluster-kubeconfig derivation.
func deriveKubeconfigFromBundle(bundle *secrets.Bundle, clusterName, endpoint string) ([]byte, error) {
	if bundle == nil || bundle.Certs == nil || bundle.Certs.K8s == nil {
		return nil, fmt.Errorf("secrets bundle has no kubernetes CA")
	}
	ca, err := cryptox509.NewCertificateAuthorityFromCertificateAndKey(bundle.Certs.K8s)
	if err != nil {
		return nil, fmt.Errorf("loading kubernetes CA from bundle: %w", err)
	}
	now := time.Now()
	admin, err := cryptox509.NewKeyPair(ca,
		cryptox509.CommonName("admin"),
		cryptox509.Organization("system:masters"),
		cryptox509.NotBefore(now.Add(-time.Hour)),
		cryptox509.NotAfter(now.Add(adminKubeconfigValidity)),
		cryptox509.KeyUsage(stdx509.KeyUsageDigitalSignature|stdx509.KeyUsageKeyEncipherment),
		cryptox509.ExtKeyUsage([]stdx509.ExtKeyUsage{stdx509.ExtKeyUsageClientAuth}),
	)
	if err != nil {
		return nil, fmt.Errorf("minting admin client certificate: %w", err)
	}

	name := "admin@" + clusterName
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {Server: endpoint, CertificateAuthorityData: bundle.Certs.K8s.Crt},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			name: {ClientCertificateData: admin.CrtPEM, ClientKeyData: admin.KeyPEM},
		},
		Contexts: map[string]*clientcmdapi.Context{
			name: {Cluster: clusterName, AuthInfo: name},
		},
		CurrentContext: name,
	}
	out, err := clientcmd.Write(cfg)
	if err != nil {
		return nil, fmt.Errorf("writing kubeconfig: %w", err)
	}
	return out, nil
}
