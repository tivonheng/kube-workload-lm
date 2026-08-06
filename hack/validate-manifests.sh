#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if command -v kubectl >/dev/null 2>&1; then
  kubectl kustomize "${root}/config/base" >/dev/null
  kubectl kustomize "${root}/config/samples" >/dev/null
elif command -v kustomize >/dev/null 2>&1; then
  kustomize build "${root}/config/base" >/dev/null
  kustomize build "${root}/config/samples" >/dev/null
else
  echo "kubectl or kustomize is required to render manifests" >&2
  exit 1
fi

go -C "${root}" test ./config
