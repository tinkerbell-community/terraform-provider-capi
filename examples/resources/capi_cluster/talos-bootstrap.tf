# A Tinkerbell cluster whose bootstrap (management) cluster is a single
# bare-metal machine provisioned with Talos through its BMC instead of kind.
# After the self-managed pivot the node is reset, powered off, and free for
# CAPT to reclaim into the workload cluster.
resource "capi_cluster" "bare_metal" {
  name               = "bm-cluster"
  kubernetes_version = "v1.34.0"

  infrastructure = {
    provider = "tinkerbell:v0.5.4"
  }

  bootstrap = {
    provider = "talos:v0.6.7"
  }

  control_plane = {
    provider      = "talos:v0.6.7"
    machine_count = 3
  }

  workers = {
    machine_count = 2
  }

  management = {
    self_managed = true

    bootstrap = {
      type    = "talos"
      machine = "cp-1"

      boot = {
        method   = "auto" # virtual media first, then UEFI HTTP boot
        timeout  = "15m"
        attempts = 3
      }

      talos = {
        version      = "v1.13.6"
        architecture = "amd64"

        image = {
          extensions  = ["siderolabs/iscsi-tools"]
          kernel_args = ["net.ifnames=0"]
        }

        config_patches = [file("${path.module}/cni-none.yaml")]
      }

      addons = {
        helm = [
          {
            name      = "cilium"
            namespace = "kube-system"
            chart     = "oci://quay.io/cilium/charts/cilium"
            version   = "1.18.0"
            values = yamlencode({
              kubeProxyReplacement = true
              k8sServiceHost       = "localhost"
              k8sServicePort       = 7445
              ipam                 = { mode = "kubernetes" }
            })
          }
        ]
      }
    }
  }

  inventory = {
    machine = [
      {
        hostname = "cp-1"
        network = {
          ip_address  = "192.168.1.10"
          netmask     = "255.255.255.0"
          gateway     = "192.168.1.1"
          mac_address = "aa:bb:cc:dd:ee:01"
        }
        disk = { device = "/dev/nvme0n1" }
        bmc = {
          address  = "192.168.2.10"
          username = "admin"
          password = var.bmc_password
        }
        labels = { type = "cp" }
      },
    ]
  }
}

variable "bmc_password" {
  type      = string
  sensitive = true
}
