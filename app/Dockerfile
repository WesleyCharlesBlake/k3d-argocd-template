# syntax=docker/dockerfile:1.7
# Multi-stage build → small, non-root, no shell in the final image.

FROM golang:1.23-alpine AS builder

WORKDIR /src

# Cache module download before copying the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none

# CGO disabled → fully static binary; -s -w strips symbols/debug to shrink the image.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/myapp \
    ./

# Distroless static — no shell, no package manager, runs as nonroot.
# Production-grade base; smaller blast radius than alpine.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/myapp /app/myapp

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/app/myapp"]
