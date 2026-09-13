// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

func TestTalosSecretsBundleManifest(t *testing.T) {
	out, err := talosSecretsBundleManifest("prod", "capi-system", "cluster:\n  id: abc\n")
	if err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := sigsyaml.Unmarshal(out, &sec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sec.Name != "prod-talos" || sec.Namespace != "capi-system" {
		t.Fatalf("name/ns = %s/%s", sec.Name, sec.Namespace)
	}
	if sec.Labels[clusterNameLabel] != "prod" {
		t.Fatalf("label = %q", sec.Labels[clusterNameLabel])
	}
	if got := string(sec.Data["bundle"]); !strings.Contains(got, "id: abc") {
		t.Fatalf("bundle data = %q", got)
	}
}

func TestClusterKubeconfigManifest(t *testing.T) {
	out, err := clusterKubeconfigManifest("prod", "capi-system", []byte("apiVersion: v1\nkind: Config\n"))
	if err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := sigsyaml.Unmarshal(out, &sec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sec.Name != "prod-kubeconfig" || sec.Namespace != "capi-system" {
		t.Fatalf("name/ns = %s/%s", sec.Name, sec.Namespace)
	}
	if string(sec.Type) != kubeconfigSecretType {
		t.Fatalf("type = %q", sec.Type)
	}
	if sec.Labels[clusterNameLabel] != "prod" {
		t.Fatalf("label = %q", sec.Labels[clusterNameLabel])
	}
	if !strings.Contains(string(sec.Data["value"]), "kind: Config") {
		t.Fatalf("value data = %q", sec.Data["value"])
	}
}
