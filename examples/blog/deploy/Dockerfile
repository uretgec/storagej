# syntax=docker/dockerfile:1

# Build from the repository root (the build context must contain go.mod):
#   docker build -f examples/blog/deploy/Dockerfile -t storagej-server .
#
# Run (single tenant, data kept in a named volume):
#   docker run -p 8080:8080 -v storagej-data:/data storagej-server
#
# Multi-tenant with auth and rate limiting (extra flags are appended):
#   docker run -p 8080:8080 -v storagej-data:/data storagej-server \
#     -multi-tenant -api-keys "acme:secret1,other:secret2" -rate-limit 10

FROM golang:1.26 AS build
WORKDIR /src

# Dependencies first, so source edits don't invalidate this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/storagej-server ./examples/blog/cmd/storagej-server

# distroless has no shell, so the data dir is created here and handed to the
# nonroot user (uid 65532) via COPY --chown below.
RUN mkdir /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/storagej-server /storagej-server
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/storagej-server", "-data", "/data"]