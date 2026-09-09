terraform {
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = "0.102.0"
    }
  }
}

provider "proxmox" {
  endpoint = var.pve_endpoint
  username = var.pve_username
  password = var.pve_password
  insecure = true # self-signed cert
}
