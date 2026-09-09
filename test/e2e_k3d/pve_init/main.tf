resource "proxmox_download_file" "talos_img" {
  node_name = var.pve_node_name
  datastore_id = local.datastore_id_import

  # use a "nocloud" image with qemu-guest-agent extension
  # NOTE: we also intentionally use one version behind the one specified in the cluster specification, to test if OVA provisioning catches that it must upgrade the OS after provisioning
  # https://factory.talos.dev/?arch=amd64&bootloader=auto&cmdline-set=true&extensions=-&extensions=siderolabs%2Fqemu-guest-agent&platform=nocloud&secureboot=true&target=cloud&version=1.12.6
  url = "https://factory.talos.dev/image/ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515/v1.12.6/nocloud-amd64-secureboot.qcow2"
  file_name = "talos-v1.12.6-amd64-secureboot.qcow2"
  content_type = "import"
}
