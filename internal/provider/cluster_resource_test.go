// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccClusterResource(t *testing.T) {
	// The docker infrastructure provider's "development" flavor template is
	// ClusterClass-based, which requires the ClusterTopology feature gate to
	// be enabled on the core provider at install time.
	t.Setenv("CLUSTER_TOPOLOGY", "true")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccClusterResourceConfig("test-cluster"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("capi_cluster.test", "name", "test-cluster"),
					resource.TestCheckResourceAttr("capi_cluster.test", "infrastructure.provider", "docker"),
					resource.TestCheckResourceAttr("capi_cluster.test", "management.skip_init", "false"),
					resource.TestCheckResourceAttr("capi_cluster.test", "management.self_managed", "false"),
					resource.TestCheckResourceAttrSet("capi_cluster.test", "id"),
					resource.TestCheckResourceAttrSet("capi_cluster.test", "management.namespace"),
				),
			},
			// ImportState testing
			{
				ResourceName:      "capi_cluster.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					// flavor, kubernetes_version, and infrastructure are
					// creation-time-only template parameters: the management
					// cluster used to create the workload cluster is an
					// ephemeral bootstrap cluster torn down after apply, so
					// there is no live source to recover them from on import.
					"management", "wait", "output",
					"status", "flavor", "kubernetes_version",
					"infrastructure", "infrastructure.provider", "infrastructure.%",
				},
			},
		},
	})
}

func testAccClusterResourceConfig(name string) string {
	return `
resource "capi_cluster" "test" {
  name               = "` + name + `"
  flavor             = "development"
  kubernetes_version = "v1.31.0"

  infrastructure = {
    provider = "docker"
  }

  wait = {
    enabled = false
  }
}
`
}
