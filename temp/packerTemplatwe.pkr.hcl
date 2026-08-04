variable "aws_region" {
  default = "us-east-1"
}

variable "azure_location" {
  default = "East US"
}

variable "gcp_project_id" {
  default = "your-gcp-project-id"
}

source "amazon-ebs" "nginx" {
  region     = var.aws_region
  source_ami = "ami-0c55b159cbfafe1f0" # Ubuntu 20.04 LTS
  instance_type = "t2.micro"

  ssh_username = "ubuntu"

  tags = {
    Name = "nginx-aws"
  }
}

source "azure-arm" "nginx" {
  client_id       = "<your-azure-client-id>"
  client_secret   = "<your-azure-client-secret>"
  tenant_id       = "<your-azure-tenant-id>"
  subscription_id = "<your-azure-subscription-id>"

  managed_image_name = "nginx-azure-image"
  managed_image_resource_group_name = "nginx-resource-group"
  location = var.azure_location

  os_type       = "Linux"
  image_publisher = "Canonical"
  image_offer     = "0001-com-ubuntu-server-focal"
  image_sku       = "20_04-lts-gen2"
}

source "googlecompute" "nginx" {
  project_id      = var.gcp_project_id
  source_image    = "ubuntu-2004-focal-v20240301"
  machine_type    = "e2-micro"
  zone            = "us-central1-a"
}

build {
  sources = [
    "source.amazon-ebs.nginx",
    "source.azure-arm.nginx",
    "source.googlecompute.nginx"
  ]

  provisioner "shell" {
    inline = [
      "sudo apt-get update",
      "sudo apt-get install -y nginx",
      "sudo systemctl enable nginx",
      "sudo systemctl start nginx"
    ]
  }
}
