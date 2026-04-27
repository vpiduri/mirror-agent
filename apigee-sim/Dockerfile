# Combined image: APIGEE simulator + eBPF mirror agent
# Models the production setup where both run on the same VM.
#
# Build context must be the repo root so we can access:
#   kernel/         — eBPF C source
#   mirror-agent/   — Go mirror agent source
#   apigee-sim/     — Go APIGEE simulator source

# ── Stage 1: compile the eBPF C program ──────────────────────────────────────
FROM ubuntu:22.04 AS ebpf-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        clang llvm libbpf-dev linux-headers-generic linux-libc-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY kernel/mirror.bpf.c .

RUN clang -O2 -g -target bpf \
        -D__KERNEL__ \
        -I/usr/include/x86_64-linux-gnu \
        -I/usr/include \
        -c mirror.bpf.c -o mirror.bpf.o

# ── Stage 2: build both Go binaries ──────────────────────────────────────────
FROM golang:1.21-bookworm AS go-builder

# Build mirror-agent
WORKDIR /src/mirror-agent
COPY mirror-agent/go.mod mirror-agent/go.sum* ./
RUN go mod download
COPY mirror-agent/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/mirror-agent .

# Build apigee-sim
WORKDIR /src/apigee-sim
COPY apigee-sim/main.go .
# Inline go.mod for the standalone apigee-sim binary
RUN printf 'module apigee-sim\ngo 1.21\n' > go.mod && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/apigee-sim .

# ── Stage 3: runtime image ────────────────────────────────────────────────────
FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
        iproute2 libbpf0 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-builder   /out/apigee-sim    /apigee-sim
COPY --from=go-builder   /out/mirror-agent  /mirror-agent
COPY --from=ebpf-builder /build/mirror.bpf.o /ebpf/mirror.bpf.o
COPY apigee-sim/entrypoint.sh /entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
