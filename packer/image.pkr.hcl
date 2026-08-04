packer {
  required_plugins {
    amazon = {
      version = ">= 1.0.0"
      source  = "github.com/hashicorp/amazon"
    }
    googlecompute = {
      version = ">= 1.0.0"
      source  = "github.com/hashicorp/googlecompute"
    }
  }
}

// AWS builder
source "amazon-ebs" "ubuntu" {
  // Will automatically use AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_REGION
  // from environment variables
  instance_type = "t2.micro"
  ami_name      = "my-multi-cloud-image-{{timestamp}}"
  source_ami_filter {
    filters = {
      name                = "ubuntu/images/hvm-ssd/ubuntu-focal-20.04-amd64-server-*"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    most_recent = true
    owners      = ["099720109477"] // Canonical
  }
  ssh_username = "ubuntu"
}
variable "project_id" {
  type = string
  default = ""
}
// GCP builder
source "googlecompute" "ubuntu" {
  // Will automatically use GOOGLE_APPLICATION_CREDENTIALS and GOOGLE_CLOUD_PROJECT
  // from environment variables
    project_id = var.project_id
  zone         = "us-central1-a"
  source_image_family = "ubuntu-2004-lts"
  ssh_username = "ubuntu"
  image_name   = "my-multi-cloud-image-{{timestamp}}"
  image_description = "Ubuntu image for multi-cloud deployment"
}

// Common build configuration
build {
  name = "multi-cloud-image"
  
  sources = [
    "source.amazon-ebs.ubuntu",
    "source.googlecompute.ubuntu"
  ]

  // Install common software
  provisioner "shell" {
    inline = [
      "echo Installing dependencies...",
      "sudo apt-get update",
      "sudo apt-get install -y nginx docker.io",
      "echo 'Hello from Packer!' > index.html",
      "sudo mv index.html /var/www/html/"
    ]
  }
}