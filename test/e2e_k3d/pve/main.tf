data "proxmox_file" "talos_img" {
  node_name = var.pve_node_name
  datastore_id = local.datastore_id_import
  content_type = "import"
  file_name = "talos-v1.12.6-amd64-secureboot.qcow2" # match ../pve_init/main.tf
}

resource "proxmox_virtual_environment_vm" "controlplane" {
  count = 3

  node_name = var.pve_node_name

  name = "pandora-c${count.index + 1}"
  bios = "ovmf"
  efi_disk {
    datastore_id = local.datastore_id_vm
  }
  cpu {
    sockets = 1
    cores = 2
    type = "host"
  }
  memory {
    dedicated = 2048
    floating = 4096
  }
  network_device {
    bridge = "vmbr0" # remember to enable VLAN aware mode on the bridge if this isn't working for you
    vlan_id = 20
  }
  disk {
    datastore_id = local.datastore_id_vm
    interface = "scsi0"
    import_from = data.proxmox_file.talos_img.id
    size = 32
  }
  agent {
    enabled = true
  }
}

resource "proxmox_virtual_environment_vm" "worker" {
  count = 3

  node_name = var.pve_node_name

  name = "pandora-w${count.index + 1}"
  bios = "ovmf"
  efi_disk {
    datastore_id = local.datastore_id_vm
  }
  cpu {
    sockets = 1
    cores = 4
    type = "host"
  }
  memory {
    dedicated = 2048
    floating = 8192
  }
  network_device {
    bridge = "vmbr0"
    vlan_id = 20
  }
  disk {
    datastore_id = local.datastore_id_vm
    interface = "scsi0"
    import_from = data.proxmox_file.talos_img.id
    size = 64
  }
  agent {
    enabled = true
  }
}
