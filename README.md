# Multi-Cloud Spot Instance Recommender

A Go prototype that recommends cloud VM instances from a local catalogue, then provides scripts for deploying an image to AWS, Azure, or Google Cloud. It also includes Packer configuration for building a shared AWS/GCP image and a Bash preemption-check script intended for spot/preemptible instances.

> **Project status:** prototype / academic project. Review the [known limitations](#known-limitations) before using it for deployments.

## What it does

- Loads instance specifications from `spec_map.json`.
- Collects desired vCPUs, memory, and maximum hourly price in a terminal UI.
- Normalizes those attributes and ranks the five nearest matches with Euclidean distance.
- Uses `deployinstance.sh` to invoke the AWS CLI, Azure CLI, or Google Cloud CLI for the first matching provider result.
- Uses `packer/image.pkr.hcl` to build a basic Ubuntu image for AWS and GCP.
- Uses `imdscheck.sh` to check each provider's metadata service for a preemption notice and, if detected, invoke the deployment script.

## Repository layout

| Path | Purpose |
| --- | --- |
| `knntui.go` | Bubble Tea terminal UI and instance-ranking logic. |
| `spec_map.json` | Local multi-cloud instance catalogue consumed by the UI. |
| `v2fetchinstance.go` | Development utility for gathering instance and pricing data from cloud APIs. |
| `deployinstance.sh` | Bash deployment helper for AWS, Azure, and GCP. |
| `imdscheck.sh` | Bash metadata/preemption monitor. |
| `packer/image.pkr.hcl` | Packer sources and provisioning for AWS and GCP images. |
| `.env.example` | Safe local deployment configuration template. |
| `packer/.env.example` | Safe local Packer configuration template. |

## Prerequisites

- Go **1.23.8** (the toolchain declared in `go.mod`)
- A Bash environment for the deployment and preemption scripts (for example WSL or Git Bash on Windows)
- The cloud CLIs required by `deployinstance.sh`:
  - [AWS CLI](https://aws.amazon.com/cli/)
  - [Azure CLI](https://learn.microsoft.com/cli/azure/)
  - [Google Cloud CLI](https://cloud.google.com/sdk/docs/install)
- Cloud credentials configured through each CLI or workload identity, with permission to create the relevant resources
- [Packer](https://developer.hashicorp.com/packer/install) for image builds

## Quick start

### 1. Get the code and dependencies

```powershell
git clone <repository-url>
Set-Location MinorProject
go mod download
go build knntui.go
```

### 2. Create local deployment configuration

Create a local `.env` from the tracked template. It contains regions and the image IDs that the deployment script uses.

```powershell
Copy-Item .env.example .env
```

Edit `.env` and replace every `REPLACE_WITH_...` value. Do **not** commit this file.

```dotenv
AWS_DEFAULT_REGION=us-east-1
AZURE_DEFAULT_REGION=eastus
GCP_DEFAULT_REGION=us-central1-a
AWS_IMAGE_ID=ami-REPLACE_WITH_YOUR_IMAGE_ID
AZURE_IMAGE_ID=REPLACE_WITH_YOUR_AZURE_IMAGE_ID
GCP_IMAGE_ID=REPLACE_WITH_YOUR_GCP_IMAGE_ID
```

### 3. Run the recommender

```powershell
go run knntui.go
```

Enter the requested vCPU count, memory in GB, and maximum hourly price. Press **Enter** after each field to see up to five closest catalogue matches. Press `q` or `Ctrl+C` to exit.

## Deployment workflow

`deployinstance.sh` starts the TUI, captures its displayed result, identifies a provider and instance type, then calls the corresponding cloud CLI.

Before running it:

1. Configure and test `aws`, `az`, and `gcloud` authentication. The current script checks for all three CLIs, even if only one provider will be selected.
2. Create `.env` as described above and set the correct image IDs.
3. Update `TUI_PATH` in `deployinstance.sh`. It currently contains an author-specific WSL path and must point to this checkout's `knntui.go` file.

From Bash/WSL:

```bash
bash ./deployinstance.sh
```

Deployments create billable cloud resources. Confirm regions, image IDs, account/project selection, quotas, and CLI permissions before executing the script.

## Building images with Packer

The current Packer definition creates a basic Ubuntu 20.04 image with Nginx and Docker for **AWS and GCP**. Azure image building is not yet configured.

1. Create your local Packer configuration:

   ```bash
   cp packer/.env.example packer/.env
   ```

2. Set the placeholder values and authenticate with a named AWS profile, short-lived credentials, or workload identity. For GCP, set `GOOGLE_APPLICATION_CREDENTIALS` to a local service-account key path or use an alternative supported authentication mechanism.

3. Load the variables and run Packer:

   ```bash
   set -a
   . packer/.env
   set +a
   packer init packer/image.pkr.hcl
   packer validate packer/image.pkr.hcl
   packer build packer/image.pkr.hcl
   ```

Record the resulting image IDs in the root `.env` file before attempting deployment.

## Preemption monitoring

`imdscheck.sh` is designed to run within a cloud VM. It queries the provider metadata service for a preemption/eviction signal and runs `deployinstance.sh` when it detects one.

```bash
bash ./imdscheck.sh
```

Run it only after configuring the same local deployment prerequisites on that VM. Test it in a non-production environment first: a metadata-service connectivity failure can be interpreted as a terminated instance by the current implementation.

## Security and configuration

- Keep secrets, tokens, service-account files, and local `.env` files out of Git.
- Copy from `.env.example` and `packer/.env.example`; commit only templates with placeholders.
- Prefer short-lived credentials, named CLI profiles, instance roles, workload identity, or a secret manager over long-lived access keys.
- If a credential was ever committed, revoke or rotate it immediately. Removing a file from the latest commit does not remove it from prior Git history.

## Development notes

`v2fetchinstance.go` is a development data-collection utility. It queries cloud APIs and writes `spec_map.json`; it requires valid cloud authentication and currently includes provider-specific assumptions, including fixed Azure and GCP identifiers. Review and parameterize it before running it against another account or project.

## Known limitations

- The application has no automated test suite or CI workflow yet.
- `go test ./...` is currently blocked by an incomplete Go file under `temp/`; `go build knntui.go` succeeds.
- The UI exits immediately after producing results, so its result-navigation code is not currently reachable.
- Numeric input parsing does not yet provide validation feedback.
- `deployinstance.sh` parses terminal display text instead of consuming structured application output, which is fragile.
- The deployment script lacks strict shell error handling and can report success after a provider CLI failure.
- Packer supports AWS and GCP only, while the deployment script also supports Azure.
- Packer plugin versions are open-ended and the base Ubuntu 20.04 images should be reviewed before production use.

## Suggested next improvements

1. Split the Go programs into explicit `cmd/` packages and move experiments out of the main module.
2. Return structured JSON from the recommender and make deployment consume that data rather than terminal text.
3. Add input validation, unit tests for ranking and configuration, and CI for formatting, vetting, and tests.
4. Add Azure Packer support and pin plugin/image versions.
5. Replace local credential handling with cloud-native identities or a secret manager.
