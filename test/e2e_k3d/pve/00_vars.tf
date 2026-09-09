variable "pve_endpoint" {
  type = string
}

variable "pve_username" {
  type = string
}

variable "pve_password" {
  type = string
  sensitive = true
}

variable "pve_node_name" {
  type = string
  default = "miranda-h1"
}
