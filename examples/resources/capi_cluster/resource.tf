resource "capi_cluster" "example" {
  name               = "my-cluster"
  kubernetes_version = "v1.31.0"

  infrastructure = {
    docker = {}
  }

  bootstrap = {
    kubeadm = {}
  }

  control_plane = {
    kubeadm = {}
  }

  topology {
    control_plane_count = 1
    worker_count        = 2
  }

  wait {
    enabled = true
    timeout = "30m"
  }

  output {
    kubeconfig_path = "/tmp/my-cluster-kubeconfig"
  }
}
