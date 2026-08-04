#!/bin/bash

# check-preemption.sh - Script to detect preemption across cloud providers and redeploy if needed

# Load environment variables
source .env

# Path to deployment script
DEPLOY_SCRIPT="./deployinstance.sh"

# Log function
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1"
}

# Check if deployment script exists and is executable
if [ ! -x "$DEPLOY_SCRIPT" ]; then
    log "Error: Deployment script not found or not executable at $DEPLOY_SCRIPT"
    exit 1
fi

# Check AWS instance preemption
check_aws_preemption() {
    log "Checking AWS instance status..."
    
    # Try to access the instance metadata service
    # If the request times out or returns an error, the instance may have been preempted
    AWS_METADATA=$(curl -s --connect-timeout 2 http://169.254.169.254/latest/meta-data/spot/instance-action 2>/dev/null)
    
    if [ $? -eq 0 ] && [ ! -z "$AWS_METADATA" ]; then
        log "AWS instance is being preempted: $AWS_METADATA"
        return 0 # Preempted
    else
        # Also check if instance is accessible at all
        INSTANCE_ID=$(curl -s --connect-timeout 2 http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null)
        if [ $? -ne 0 ] || [ -z "$INSTANCE_ID" ]; then
            # Could not access metadata service, but we're on AWS (check cloud-specific file)
            if [ -f /sys/hypervisor/uuid ] && grep -qi "ec2" /sys/hypervisor/uuid; then
                log "AWS metadata service inaccessible, instance may be terminated"
                return 0 # Preempted/terminated
            fi
        fi
    fi
    
    log "AWS instance is running normally"
    return 1 # Not preempted
}

# Check Azure instance preemption
check_azure_preemption() {
    log "Checking Azure instance status..."
    
    # Try to access the instance metadata service
    # Check Scheduled Events API for preemption notifications
    AZURE_METADATA=$(curl -s -H "Metadata:true" --connect-timeout 2 "http://169.254.169.254/metadata/instance/compute/evictionPolicy?api-version=2021-01-01&format=text" 2>/dev/null)
    
    if [ $? -eq 0 ] && [ "$AZURE_METADATA" == "Deallocate" ]; then
        # Check scheduled events
        EVENTS=$(curl -s -H "Metadata:true" --connect-timeout 2 "http://169.254.169.254/metadata/scheduledevents?api-version=2020-07-01" 2>/dev/null)
        if [[ "$EVENTS" == *"Preempt"* ]]; then
            log "Azure instance is being preempted"
            return 0 # Preempted
        fi
    else
        # Also check if instance is accessible at all
        if [ -f /sys/class/dmi/id/chassis_asset_tag ] && grep -qi "7783-7084-3265-9085-8269-3286-77" /sys/class/dmi/id/chassis_asset_tag; then
            INSTANCE_ID=$(curl -s -H "Metadata:true" --connect-timeout 2 "http://169.254.169.254/metadata/instance/compute/vmId?api-version=2021-01-01&format=text" 2>/dev/null)
            if [ $? -ne 0 ] || [ -z "$INSTANCE_ID" ]; then
                log "Azure metadata service inaccessible, instance may be terminated"
                return 0 # Preempted/terminated
            fi
        fi
    fi
    
    log "Azure instance is running normally"
    return 1 # Not preempted
}

# Check GCP instance preemption
check_gcp_preemption() {
    log "Checking GCP instance status..."
    
    # Try to access the instance metadata service
    # Check for preemption notice
    GCP_METADATA=$(curl -s --connect-timeout 2 "http://metadata.google.internal/computeMetadata/v1/instance/preempted" -H "Metadata-Flavor: Google" 2>/dev/null)
    
    if [ $? -eq 0 ] && [ "$GCP_METADATA" == "TRUE" ]; then
        log "GCP instance is being preempted"
        return 0 # Preempted
    else
        # Also check if instance is accessible at all
        if [ -f /sys/class/dmi/id/product_name ] && grep -qi "Google" /sys/class/dmi/id/product_name; then
            INSTANCE_ID=$(curl -s --connect-timeout 2 "http://metadata.google.internal/computeMetadata/v1/instance/id" -H "Metadata-Flavor: Google" 2>/dev/null)
            if [ $? -ne 0 ] || [ -z "$INSTANCE_ID" ]; then
                log "GCP metadata service inaccessible, instance may be terminated"
                return 0 # Preempted/terminated
            fi
        fi
    fi
    
    log "GCP instance is running normally"
    return 1 # Not preempted
}

# Detect which cloud provider we're on and check preemption
check_preemption() {
    # Try to determine which cloud we're on
    if curl -s --connect-timeout 1 http://169.254.169.254/latest/meta-data/ &>/dev/null; then
        # AWS metadata service responded
        check_aws_preemption
        return $?
    elif curl -s -H "Metadata:true" --connect-timeout 1 "http://169.254.169.254/metadata/instance?api-version=2021-01-01" &>/dev/null; then
        # Azure metadata service responded
        check_azure_preemption
        return $?
    elif curl -s --connect-timeout 1 "http://metadata.google.internal/computeMetadata/v1/" -H "Metadata-Flavor: Google" &>/dev/null; then
        # GCP metadata service responded
        check_gcp_preemption
        return $?
    else
        # Check for cloud-specific files as a fallback
        if [ -f /sys/hypervisor/uuid ] && grep -qi "ec2" /sys/hypervisor/uuid; then
            check_aws_preemption
            return $?
        elif [ -f /sys/class/dmi/id/chassis_asset_tag ] && grep -qi "7783-7084-3265-9085-8269-3286-77" /sys/class/dmi/id/chassis_asset_tag; then
            check_azure_preemption
            return $?
        elif [ -f /sys/class/dmi/id/product_name ] && grep -qi "Google" /sys/class/dmi/id/product_name; then
            check_gcp_preemption
            return $?
        else
            log "Could not determine cloud provider or not running in a cloud environment"
            return 2  # Unknown
        fi
    fi
}

# Main execution
log "Starting preemption check..."

# Run the check
check_preemption
PREEMPTION_STATUS=$?

if [ $PREEMPTION_STATUS -eq 0 ]; then
    log "Instance preemption detected! Initiating redeployment..."
    # Run deployment script
    bash "$DEPLOY_SCRIPT"
    DEPLOY_STATUS=$?
    
    if [ $DEPLOY_STATUS -eq 0 ]; then
        log "Successfully redeployed instance"
    else
        log "Failed to redeploy instance, exit code: $DEPLOY_STATUS"
        exit $DEPLOY_STATUS
    fi
elif [ $PREEMPTION_STATUS -eq 2 ]; then
    log "Not running in a known cloud environment. No action taken."
else
    log "No preemption detected. Instance is running normally."
fi

log "Preemption check completed"
exit 0