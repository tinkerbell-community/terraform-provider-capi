// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

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
