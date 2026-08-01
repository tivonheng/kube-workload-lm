# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.24.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -buildid=" -o /out/controller ./cmd/controller

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/controller /controller
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/controller"]
