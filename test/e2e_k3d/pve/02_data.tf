# identify datastores for our VM template and VM disks
data "proxmox_datastores" "datastores_import" {
  node_name = var.pve_node_name
  filters = {
    content_types = ["import"]
  }
}

data "proxmox_datastores" "datastores_images" {
  node_name = var.pve_node_name
  filters = {
    content_types = ["images"]
  }
}

locals {
  # pick one "stable" datastore
  datastore_id_import = sort([for x in data.proxmox_datastores.datastores_import.datastores : x.id])[0]
  datastore_id_vm = sort([for x in data.proxmox_datastores.datastores_images.datastores : x.id])[0]
}
