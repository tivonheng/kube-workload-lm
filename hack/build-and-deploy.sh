#!/bin/sh
set -eu

# --- Configuration ---
REGISTRY="557690602572.dkr.ecr.eu-central-1.amazonaws.com"
REPO="kube-workload-lifecycle-manager"
REGION="eu-central-1"
AWS_PROFILE="osl-pay-test-admin"
CONTAINER_ENGINE="${CONTAINER_ENGINE:-podman}"

# --- Derive tag from git commit ---
TAG=$(git rev-parse --short HEAD)
IMG="${REGISTRY}/${REPO}:${TAG}"

echo "==> Building image: ${IMG}"
${CONTAINER_ENGINE} build --platform linux/amd64 \
  --build-arg GO_VERSION=1.24.0 \
  --tag "${IMG}" .

echo "==> Logging in to ECR (${REGION})..."
aws-vault exec "${AWS_PROFILE}" -- \
  aws ecr get-login-password --region "${REGION}" | \
  ${CONTAINER_ENGINE} login --username AWS --password-stdin "${REGISTRY}"

echo "==> Pushing image: ${IMG}"
${CONTAINER_ENGINE} push "${IMG}"

echo "==> Generating deploy.yaml..."
kubectl kustomize config/base > deploy.yaml

# Replace image reference in deploy.yaml
sed -i '' "s|image: .*${REPO}:.*|image: ${IMG}|" deploy.yaml

echo "==> Done. Image: ${IMG}"
echo "    deploy.yaml updated with new image reference."
