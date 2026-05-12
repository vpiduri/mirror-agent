# Testing the eBPF Traffic Mirror

## Architecture (what you're testing)

```
  ┌──────────────────────── apigee container  (= APIGEE VM) ───────────────────────┐
  │                                                                                  │
  │  eth0 ──► [TC ingress eBPF hook] ──► apigee-sim process (:9090)  ← normal path │
  │                      │                                                           │
  │                 (mirror copy)                                                    │
  └──────────────────────┼───────────────────────────────────────────────────────────┘
                         │  async, non-blocking, APIGEE unaware
                         ▼
             ewp-sim — Enterprise Web Proxy (:9091)
                         │
             GET /validation-report  ← per-request pass/fail + latency
```

Both processes (APIGEE simulator and eBPF mirror agent) run **inside the same container**, exactly as they would run as two processes on the same production VM. The container handles its own BPF filesystem mount internally — no host-side setup is required.

---

## Scope of Comparison and Known Limitations

### What this tool compares

| Signal | How it is measured |
|---|---|
| HTTP response status code class (2xx / 4xx / 5xx) | Every mirrored request is replayed to EWP; the response code determines pass/fail |
| Response latency | Per-endpoint average latency from EWP is tracked in ms |
| Endpoint availability | A 404 from EWP means the route is not implemented yet |
| Response headers returned by EWP | Aggregated per endpoint in `response_headers_observed` |

### What this tool does NOT compare

| Gap | Explanation |
|---|---|
| APIGEE response headers vs EWP response headers | The eBPF hook captures client→APIGEE **ingress** only. APIGEE's outbound responses to clients are not captured, so a header-by-header diff between the two proxies is not possible without adding a separate TC **egress** hook on the same interface. |
| Response body content or JSON schema | EWP may return structurally different payloads (different field names, added or removed fields) while still returning a 2xx status code. This passes the mirror validation but may break API consumers. A contract test suite is needed for body comparison. |
| Request headers visible to the API provider (backend) app | Envoy/EWP injects headers before forwarding to the backend — `x-forwarded-for`, `x-envoy-*`, `grpc-status`, JWT claim headers, etc. The backend application may behave differently because of these additions, and that difference is invisible to this tool. |
| Response headers visible to the API consumer (client) app | Envoy adds consumer-visible headers such as `x-envoy-upstream-service-time`, `x-request-id`, and `server: envoy`, and may strip or rewrite APIGEE-specific headers. Client applications that parse, log, or validate response headers may break even when all status codes pass. |

### POD → POA proxy migration context

> **Note to engineering teams evaluating this tool for the APIGEE → EWP migration:**
>
> This mechanism validates **proxy-layer behavior** between the **POD (Point-of-Departure) APIGEE proxy** and the **POA (Point-of-Arrival) EWP proxy**. A passing result (`ready_to_cut: true`) confirms that EWP correctly routes the same traffic and returns equivalent HTTP status codes for every mirrored endpoint.
>
> **It does not confirm that the migration is safe at the API provider app layer or the API consumer app layer.**
>
> - **API provider app layer** — the backend service that APIGEE/EWP proxies to. Envoy's request transformation (auth headers, x-forwarded-for, grpc metadata) differs from APIGEE's. Backend behaviour changes driven by these header differences will not show up in this tool. Validate separately with integration tests that exercise the backend directly through EWP.
>
> - **API consumer app layer** — the client application calling the API. New response headers injected by Envoy (e.g. `x-envoy-upstream-service-time`, security headers, modified `server` header), changes to error response bodies, or differences in OAuth/JWT flows are all invisible to this tool. Validate separately with canary traffic + client-side monitoring or contract tests.
>
> In short: use this tool to confirm **EWP can handle the same requests as APIGEE**. Use integration and contract tests to confirm **nothing breaks above or below the proxy layer**.

---

## Option 1: WSL2 (Windows with WSL2 + Docker Desktop)

### Prerequisites
- Windows 10/11 with WSL2 enabled
- Docker Desktop for Windows with the **WSL2 backend** (Settings → General → "Use the WSL 2 based engine")
- WSL2 distro: Ubuntu 22.04 recommended

### Steps

Open a WSL2 terminal:

```bash
# 1. Verify the kernel supports eBPF ring buffer (needs >= 5.8)
uname -r
# Expected: 5.15.x-microsoft-standard-WSL2

# 2. Verify BTF is available (needed for eBPF type checking)
ls /sys/kernel/btf/vmlinux
# Expected: the file exists

# Note: /sys/fs/bpf being empty is normal on WSL2 — the container mounts
# its own BPF filesystem internally. No host-side mount is required.

# 3. Navigate to the project
cd /mnt/c/Users/<you>/Documents/sre-frameworks/ebpf

# 4. Optional: run the prereq check
bash scripts/check-prereqs.sh

# 5. Build all images
# (eBPF C code is compiled inside the Docker build stage — no local clang needed)
docker compose build

# 6. Start everything
docker compose up -d

# 7. Follow logs — APIGEE sim and mirror agent both log to the apigee container
docker compose logs -f apigee
# In a second terminal, watch EWP receive the mirrored copies:
docker compose logs -f ewp-sim

# 8. Run the test suite
make test-traffic

# 9. Check the migration validation report
make report
```

### WSL2 troubleshooting

| Symptom | Fix |
|---|---|
| `apigee` container exits immediately | `docker compose logs apigee` — look for capability errors or eBPF load failures |
| `tc filter add: operation not permitted` | Docker Desktop needs privileged container support — check Settings → Docker Engine |
| `bpf_ringbuf_reserve` fails | Ring buffer needs kernel ≥ 5.8. WSL2 5.15 is fine; run `uname -r` to confirm |
| `/sys/fs/bpf` empty on host | Expected on WSL2 — the container mounts its own bpffs. No action needed. |
| `CAP_BPF: unknown capability` | Older Docker — remove `- BPF` from `cap_add` in docker-compose.yml |
| Mirror agent starts but no traffic mirrored | Exec into container and check TC hook: `docker exec apigee tc filter show dev eth0 ingress` |

---

## Option 2: Mac (Docker Desktop for Mac / Colima)

On Mac, Docker containers run inside a **Linux VM** managed by Docker Desktop or Colima. The eBPF program runs inside that Linux VM — the Mac kernel is not involved.

### With Docker Desktop for Mac

```bash
# 1. Install Docker Desktop for Mac
#    Settings → Resources: allocate at least 2 CPU, 4 GB memory

# 2. Verify the Linux VM kernel version
docker run --rm alpine uname -r
# Expected: 5.15+ or 6.x

# 3. Navigate to the project
cd ~/path/to/sre-frameworks/ebpf

# 4. Build and start
docker compose build
docker compose up -d

# 5. Test
make test-traffic
make report
```

### With Colima (recommended — more control over the Linux VM)

```bash
# Install Colima
brew install colima docker

# Start Colima with enough resources
colima start --cpu 2 --memory 4 --vm-type vz --vz-rosetta

# Verify Linux VM kernel
colima ssh -- uname -r

# Build and run (same as above)
docker compose build
docker compose up -d
make test-traffic
make report
```

### Mac troubleshooting

| Symptom | Fix |
|---|---|
| `CAP_BPF: unknown capability` | Remove `- BPF` from `cap_add` in docker-compose.yml (older Docker Desktop) |
| `apigee` container unhealthy | `docker compose logs apigee` — check if mirror agent failed to attach TC hook |
| Build fails on Apple Silicon | Add `--platform linux/amd64` to the build: `docker compose build --platform linux/amd64` |

**Apple Silicon note:** The eBPF object is compiled for the BPF virtual machine (not x86 or ARM) — it is architecture-independent and runs on both Intel and Apple Silicon Linux kernels. The Go binary is built for the container's Linux/amd64 target automatically by the multi-stage Dockerfile.

---

## Option 3: Linux VM (production — APIGEE and mirror agent on the same VM)

This is the production deployment path. The mirror agent runs as a systemd service directly on the APIGEE VM — no containers involved.

### One-time setup

```bash
# On the APIGEE VM (Ubuntu 22.04 / RHEL 8+ / Debian 11+)

# 1. Verify kernel version
uname -r    # need >= 5.8

# 2. Run the deployment script
#    Compiles the eBPF program, builds the Go binary, installs a systemd service.
sudo bash scripts/deploy-on-vm.sh \
    --iface eth0 \
    --apigee-port 9090 \
    --ewp http://<ewp-host>:9091

# 3. Verify both processes are running
sudo systemctl status ebpf-mirror
sudo journalctl -fu ebpf-mirror
```

### Manual steps (if you prefer not to use the script)

```bash
# Install deps
sudo apt-get install -y clang llvm libbpf-dev linux-headers-generic golang-go iproute2

# Compile BPF program
clang -O2 -g -target bpf \
    -I/usr/include/x86_64-linux-gnu \
    -c kernel/mirror.bpf.c -o mirror.bpf.o

# Build the agent
cd mirror-agent && go build -o mirror-agent .

# Run the agent (needs root or CAP_NET_ADMIN + CAP_SYS_ADMIN + CAP_BPF)
sudo ./mirror-agent \
    -iface eth0 \
    -port 9090 \
    -ewp http://<ewp-host>:9091 \
    -bpf ./mirror.bpf.o
```

### Finding the right interface

```bash
# List all interfaces
ip link show

# Find which interface receives traffic on APIGEE's port
ss -tlnp | grep 9090           # shows which address APIGEE is bound to
ip route get <client-ip>       # shows which interface client traffic arrives on
```

### APIGEE on multiple ports (e.g. HTTP + HTTPS)

```bash
# Run a second agent instance for port 8443
sudo ./mirror-agent -iface eth0 -port 8443 -ewp http://<ewp-host>:9091 -bpf ./mirror.bpf.o &
```

### Stopping the mirror once cutover is complete

```bash
sudo systemctl stop ebpf-mirror
sudo systemctl disable ebpf-mirror

# Confirm no TC filters remain on the interface
sudo tc filter show dev eth0 ingress
```

---

## Verifying the eBPF hook is active

### In Docker (WSL2 / Mac)

```bash
# Exec into the apigee container — both APIGEE and the mirror agent run here
docker exec -it apigee bash

# Inside the container:
tc filter show dev eth0 ingress
# Expected:
#   filter protocol all pref 1 bpf chain 0 handle 0x1 mirror_ingress direct-action ...

# Optional: inspect BPF maps (if bpftool is available)
bpftool map list
bpftool map dump name cfg_target_port   # should show port 9090
bpftool map dump name counters          # matched packets + ringbuf drops
```

### On a Linux VM

```bash
# Run directly on the APIGEE VM
tc filter show dev eth0 ingress

bpftool map list
bpftool map dump name counters
```

---

## Quick smoke test sequence

```bash
# 1. Send a request to APIGEE
curl http://localhost:9090/api/v1/products

# 2. Check EWP received the mirrored copy
curl http://localhost:9091/validation-report

# 3. Check the apigee container logs (both APIGEE and mirror agent log here)
docker compose logs apigee | tail -20
# Look for:
#   [APIGEE] GET /api/v1/products -> 200 (1ms)
#   TCP assembled 1 request(s) from 172.28.0.x:NNNNN
#   MIRROR OK  GET /api/v1/products  status=200  latency=3ms

# 4. Check EWP logs to confirm the shadow request arrived
docker compose logs ewp-sim | tail -20
# Look for:
#   [EWP] [SHADOW] GET /api/v1/products from 172.28.0.10:NNNNN

# 5. Mirror agent stats are printed every 30 seconds and on shutdown:
#   STATS  packets(recv=12 drop=2)  streams(reaped=0 parseErr=0 assembled=4)
#          mirrors(sent=4 2xx=4 4xx=0 5xx=0 netErr=0)
```

---

## Cutover decision criteria

| Metric | Green | Yellow | Red |
|---|---|---|---|
| EWP pass rate | ≥ 99% | 95–99% | < 95% |
| EWP avg latency | ≤ 2× APIGEE | 2–5× APIGEE | > 5× APIGEE |
| EWP 5xx rate | 0% | < 1% | ≥ 1% |
| Sample size | ≥ 1000 requests | 100–999 | < 100 |

Check `GET /validation-report` on EWP for per-path breakdown and latency data. When `ready_to_cut: true` and all metrics are green for a sustained period (recommend 24h of production traffic), it is safe to route live traffic directly to EWP.
