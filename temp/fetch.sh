#! /usr/bin/env bash
mkdir -p ./out
uv run azure.py 1> ./out/azure.json
aws ec2 describe-spot-price-history   --instance-types m5.large   --product-descriptions "Linux/UNIX"   --start-time $(date -u +"%Y-%m-%dT%H:%M:%SZ" -d "1 minute ago")   --end-time $(date -u +"%Y-%m-%dT%H:%M:%SZ" -d "now")   --region us-east-1   --output json 1> ./out/aws.json
