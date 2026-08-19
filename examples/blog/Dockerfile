# syntax=docker/dockerfile:1

# --- build stage ---------------------------------------------------------
FROM golang:1.22-bookworm AS builder
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 gives a fully static binary, which is what lets the final
# stage be a shell-less distroless image. This SHOULD work: Pebble's own
# code is pure Go, and pebble.Open here never configures zstd compression
# (it uses Pebble's default, Snappy — also pure Go). But go.mod pulls in
# github.com/DataDog/zstd as an indirect dependency of Pebble, and that
# package normally wraps zstd via cgo. If this step fails with a cgo/cc
# error, that's why — switch to the fallback build below and change the
# final stage's base image accordingly (see the comment there).
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/storagej-server \
    ./cmd/storagej-server

# --- fallback build, if the static build above fails on zstd/cgo ---------
# RUN CGO_ENABLED=1 GOOS=linux go build \
#     -trimpath -ldflags="-s -w" \
#     -o /out/storagej-server \
#     ./cmd/storagej-server
# (then use `gcr.io/distroless/base-debian12:nonroot` below instead of
# `static-debian12:nonroot` — the cgo binary needs libc, the static one
# doesn't)

# --- runtime stage ---------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/storagej-server /storagej-server

# Pebble's data directory. Mount a volume here in production — without one,
# all data is lost when the container is removed.
VOLUME ["/data"]

EXPOSE 8080

# No Docker HEALTHCHECK: distroless has no shell and no curl/wget to run one
# with. GET /health is the health signal — point your orchestrator
# (Kubernetes livenessProbe, a load balancer, etc.) at it directly instead.

ENTRYPOINT ["/storagej-server"]
CMD ["-addr", ":8080", "-data", "/data", "-tenant", "default", "-metrics-interval", "45s"]
