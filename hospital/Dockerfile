# syntax=docker/dockerfile:1
#
# Google Simulated Hospital, built from source.
#
# The project was archived in March 2025 and its prebuilt image lived on
# Google Container Registry, which has since shut down. Upstream builds with
# Bazel; this Dockerfile builds with plain Go modules instead, which also
# gives us arm64 images for the Raspberry Pi path.

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
# Last upstream commit, and the google/fhir commit upstream's WORKSPACE pins.
ARG SIMHOSPITAL_COMMIT=e69eef7de3c01da3f110dda19200f84d7776fb6b
ARG FHIR_GO_COMMIT=a54aa66a3967470096775850602b4b06f00c3d79

WORKDIR /src
RUN git clone https://github.com/google/simhospital.git . \
 && git checkout ${SIMHOSPITAL_COMMIT}

# Upstream has no go.mod. Create one, pin the FHIR protos to the version the
# code was written against, and let `go mod tidy` resolve everything else.
RUN go mod init github.com/google/simhospital \
 && go get github.com/google/fhir/go@${FHIR_GO_COMMIT} \
 && go mod tidy

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/simulator ./cmd/simulator

# Lay out the runtime tree the way the original image did: the binary's
# default flags use paths relative to /health (configs/..., web/static).
RUN mkdir -p /out/health/web \
 && cp -r configs /out/health/configs \
 && cp -r web/static /out/health/web/static \
 && cp /out/simulator /out/health/simulator

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/health /health
WORKDIR /health
EXPOSE 8000
ENTRYPOINT ["/health/simulator"]
