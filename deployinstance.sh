#!/bin/bash

# deploy-first.sh - Completely revised version

# Load environment variables
source .env

# Path to TUI program
TUI_PATH="/mnt/c/Users/Athar/Documents/MinorProject/knntui.go"

# Check dependencies
check_dependency() {
    command -v $1 >/dev/null 2>&1 || {
        echo >&2 "Error: Required command '$1' not found"
        exit 1
    }
}
check_dependency "go"
check_dependency "aws"
check_dependency "az"
check_dependency "gcloud"

# Run TUI and capture ALL output to a file
echo "Starting instance recommender (enter requirements and press Enter)..."
go run "$TUI_PATH" | tee output.txt

# Look for a line with AWS, Azure, or GCP in the output file
if grep -q "AWS/" output.txt; then
    PROVIDER="AWS"
    INSTANCE_LINE=$(grep -m 1 "AWS/" output.txt)
    INSTANCE_TYPE=$(echo "$INSTANCE_LINE" | grep -o "AWS/[a-zA-Z0-9.]\+" | cut -d'/' -f2)
elif grep -q "Azure/" output.txt; then
    PROVIDER="Azure"
    INSTANCE_LINE=$(grep -m 1 "Azure/" output.txt)
    INSTANCE_TYPE=$(echo "$INSTANCE_LINE" | grep -o "Azure/[a-zA-Z0-9.]\+" | cut -d'/' -f2)
elif grep -q "GCP/" output.txt; then
    PROVIDER="GCP"
    INSTANCE_LINE=$(grep -m 1 "GCP/" output.txt)
    INSTANCE_TYPE=$(echo "$INSTANCE_LINE" | grep -o "GCP/[a-zA-Z0-9.]\+" | cut -d'/' -f2)
else
    echo "No provider information found in output."
    echo "Content of output file:"
    cat output.txt
    rm output.txt
    exit 1
fi

# Use default region based on provider
case $PROVIDER in
    "AWS") REGION=${AWS_DEFAULT_REGION:-"us-east-1"} ;;
    "Azure") REGION=${AZURE_DEFAULT_REGION:-"eastus"} ;;
    "GCP") REGION=${GCP_DEFAULT_REGION:-"us-central1-a"} ;;
esac

echo "Detected provider: $PROVIDER"
echo "Detected instance type: $INSTANCE_TYPE"
echo "Using region: $REGION"

# Get image ID from .env
case $PROVIDER in
    "AWS") IMAGE_ID=$AWS_IMAGE_ID ;;
    "Azure") IMAGE_ID=$AZURE_IMAGE_ID ;;
    "GCP") IMAGE_ID=$GCP_IMAGE_ID ;;
esac

# Deploy instance
echo -e "\nDeploying $PROVIDER $INSTANCE_TYPE in $REGION..."
case $PROVIDER in
    "AWS")
        aws ec2 run-instances \
            --image-id "$IMAGE_ID" \
            --instance-type "$INSTANCE_TYPE" \
            --region "$REGION"
        ;;
    "Azure")
        az vm create \
            --name "${INSTANCE_TYPE}-instance" \
            --image "$IMAGE_ID" \
            --resource-group packer-images \
            --location "$REGION" \
            --admin-username azureuser \
            --generate-ssh-keys
        ;;
    "GCP")
        gcloud compute instances create "${INSTANCE_TYPE}-instance" \
            --image "$IMAGE_ID" \
            --zone "$REGION"
        ;;
esac

# Clean up
rm output.txt
echo -e "\n✓ Deployment successful! Check cloud console for details."