# syntax=docker/dockerfile:1

# ---- build stage -------------------------------------------------------------
# --platform=$BUILDPLATFORM runs the compiler natively on your machine and
# cross-compiles for the target, so building arm64 images for a Raspberry Pi
# from an x86 or Apple Silicon laptop is fast (no emulation).
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/gateway .

# ---- runtime stage -----------------------------------------------------------
# distroless/static: no shell, no package manager, just the binary. That's why
# the healthcheck calls the binary itself instead of curl.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
USER nonroot
EXPOSE 2575 8081
ENTRYPOINT ["/gateway"]
