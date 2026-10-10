# Building and pushing the image to ECR

This is the exact, verified procedure for building the controller image and
publishing it to the `osl-pay-test` ECR registry. It uses the Makefile image
targets (default engine: Podman) and `aws-vault` for AWS credentials.

## Target registry

| Field        | Value                                                              |
|--------------|--------------------------------------------------------------------|
| Registry     | `557690602572.dkr.ecr.eu-central-1.amazonaws.com`                  |
| Image name   | `kube-workload-lifecycle-manager`                                  |
| Region       | `eu-central-1`                                                     |
| AWS account  | `557690602572`                                                     |
| aws-vault    | `osl-pay-test-admin`                                               |
| Tag          | git short SHA of the commit being released (e.g. `5fd1afb`)        |

The Makefile builds `IMG = $(REGISTRY)/$(IMAGE_NAME):$(VERSION)`, so overriding
`REGISTRY`, `IMAGE_NAME`, and `VERSION` produces the full ECR reference. Tagging
with the git short SHA keeps the image traceable to the exact source commit.

## Prerequisites

- Commit (and ideally push) your changes first, so the SHA tag corresponds to
  real source. Get it with `git rev-parse --short HEAD`.
- `podman` (5.x) and `aws-vault` installed and on `PATH`.
- The `osl-pay-test-admin` profile configured in `aws-vault` (SSO).

## Procedure

Replace `SHA` below with the release commit's short SHA.

```sh
# 0. The commit being released.
SHA=$(git rev-parse --short HEAD)

# 1. Verify the aws-vault profile resolves to the expected account (557690602572).
#    Never push without this check — it prevents pushing to the wrong account.
aws-vault exec osl-pay-test-admin -- aws sts get-caller-identity

# 2. Build the amd64 image, tagged for ECR with the git SHA.
#    The builder runs on the host arch but cross-compiles to GOARCH=amd64 via
#    --platform; the final scratch image is pure amd64, no QEMU needed.
make image-build PLATFORM=linux/amd64 \
  REGISTRY=557690602572.dkr.ecr.eu-central-1.amazonaws.com \
  IMAGE_NAME=kube-workload-lifecycle-manager \
  VERSION=$SHA

# 3. (Optional) Confirm the built image is amd64/linux and non-root.
podman image inspect \
  557690602572.dkr.ecr.eu-central-1.amazonaws.com/kube-workload-lifecycle-manager:$SHA \
  --format '{{.Architecture}} {{.Os}} {{.Config.User}}'
# Expected: amd64 linux 65532:65532

# 4. Log in to ECR (token is valid for 12h).
aws-vault exec osl-pay-test-admin -- aws ecr get-login-password --region eu-central-1 \
  | podman login --username AWS --password-stdin \
      557690602572.dkr.ecr.eu-central-1.amazonaws.com

# 5. Push.
make image-push PLATFORM=linux/amd64 \
  REGISTRY=557690602572.dkr.ecr.eu-central-1.amazonaws.com \
  IMAGE_NAME=kube-workload-lifecycle-manager \
  VERSION=$SHA

# 6. Verify the image actually landed in ECR (not just local output).
aws-vault exec osl-pay-test-admin -- aws ecr describe-images \
  --repository-name kube-workload-lifecycle-manager \
  --image-ids imageTag=$SHA \
  --region eu-central-1 \
  --query 'imageDetails[0].{Tags:imageTags,Pushed:imagePushedAt,Digest:imageDigest}'
```

## Deploying the pushed image

```sh
kubectl -n lifecycle-system set image \
  deployment/workload-lifecycle-controller \
  <container>=557690602572.dkr.ecr.eu-central-1.amazonaws.com/kube-workload-lifecycle-manager:SHA
```

In controlled environments prefer the immutable digest reported in step 6:
`...@sha256:<digest>` instead of the tag.

## Notes

- `make image-build` on an Apple Silicon host prints
  `FROM --platform=linux/arm64 golang:...` for the **build** stage — that is the
  builder image running natively; the compiled binary is still amd64 because
  `--platform linux/amd64` sets `TARGETARCH=amd64`.
- Only Make targets ending in `-push` publish; `image-build` is local-only.
- For a combined amd64+arm64 manifest, use `make image-multiarch` /
  `image-multiarch-push` with `MULTIARCH_IMG` (see the README "Container images"
  section).

## Git remotes

This repository has two remotes; pick the right one when pushing source:

| Remote   | URL                                                                   | Host   |
|----------|-----------------------------------------------------------------------|--------|
| `glb`    | `git@gitlab.glb.osl-nucleus.com:devops/kube-workload-lifecycle-manager.git` | GitLab |
| `origin` | `https://github.com/tivonheng/kube-workload-lm.git`                   | GitHub |
