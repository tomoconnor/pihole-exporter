ARG IMAGE=scratch

FROM --platform=$BUILDPLATFORM golang:1.25.5-alpine3.21 AS builder

WORKDIR /src

# Dependencies first, so a source-only change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# TARGETOS/TARGETARCH are supplied by BuildKit from --platform, so the
# builder always runs natively and only the output is cross-compiled.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags '-s -w' -o /out/pihole-exporter ./

FROM $IMAGE

LABEL org.opencontainers.image.title="pihole-exporter" \
      org.opencontainers.image.description="Prometheus exporter for Pi-hole. Maintained fork of eko/pihole-exporter." \
      org.opencontainers.image.source="https://github.com/tomoconnor/pihole-exporter" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/pihole-exporter pihole-exporter

USER 65532:65532
EXPOSE 9617

ENTRYPOINT ["/app/pihole-exporter"]
