# Testing the eBPF Traffic Mirror

## Architecture (what you're testing)

```
                ┌─────────────────────────────────── Same host / same network namespace ──┐
                │                                                                           │
  Client ──────►│  eth0  ──►  [TC ingress eBPF hook]  ──►  APIGEE (:8080)  ← normal path │
                │                        │                                                  │
                └────────────────────────┼──────────────────────────────────────────────────┘
                                         │ mirror copy (async, non-blocking)
                                         ▼
                              EWP - Enterprise Web Proxy (:8081)
                                         │
                              /validation-report  ← pass/fail rate
```

APIGEE is unaware of the mirroring. If EWP handles all requests correctly, you can cut over traffic.

---

## Option 1: WSL2 (Windows with WSL2 + Docker Desktop)

### Prerequisites
- Windows 10/11 with WSL2 enabled
- Docker Desktop for Windows with **WSL2 backend** (Settings → General → "Use the WSL 2 based engine")
- WSL2 distro: Ubuntu 22.04 recommended

### Steps

Open a WSL2 terminal:

```bash
# 1. Verify eBPF support
uname -r          # should show 5.15.x-microsoft-standard-WSL2
ls /sys/kernel/btf/vmlinux   # must exist
ls /sys/fs/bpf               # must exist

# 2. Mount BPF filesystem if not already mounted
sudo mount | grep bpf
# If not mounted:
sudo mount -t bpf bpf /sys/fs/bpf

# 3. Clone/navigate to the project
cd /mnt/c/Users/<you>/Documents/sre-frameworks/ebpf

# 4. Run the prereq check
bash scripts/check-prereqs.sh

# 5. Build all Docker images
# (eBPF C code is compiled inside the Docker build stage — no local clang needed)
docker compose build

# 6. Start everything
docker compose up -d

# 7. Watch logs — you'll see APIGEE and EWP side by side
docker compose logs -f

# In a second terminal, run the test suite
make test-traffic

# Check the EWP migration validation report
make report
```

### WSL2 troubleshooting

| Symptom | Fix |
|---|---|
| `mirror-agent` exits immediately | Check `docker compose logs mirror-agent` — likely a capability issue |
| `bpf_ringbuf_reserve` fails | Ring buffer needs kernel ≥ 5.8. WSL2 5.15 is fine. |
| `tc filter add` fails with EPERM | Docker Desktop must have privileged containers enabled |
| `/sys/fs/bpf` not found | Run `sudo mount -t bpf bpf /sys/fs/bpf` in WSL2 |
| eBPF maps not accessible | Try `docker compose run --rm --privileged mirror-agent bash` to debug interactively |

---

## Option 2: Mac (Docker Desktop for Mac / Colima)

On Mac, Docker containers run inside a **Linux VM** managed by Docker Desktop (or Colima). The eBPF program runs inside that VM — the Mac kernel itself is not involved.

### With Docker Desktop for Mac

```bash
# 1. Install Docker Desktop for Mac (Apple Silicon or Intel)
#    https://www.docker.com/products/docker-desktop/
#    Enable: Settings → Features in Development → "Enable host networking" (optional but helpful)

# 2. Clone/navigate to project
cd ~/path/to/sre-frameworks/ebpf

# 3. Check Docker VM kernel version
docker run --rm alpine uname -r
# Should be 5.15+ or 6.x

# 4. Build and run
docker compose build
docker compose up -d

# 5. Test
make test-traffic
make report
```

### With Colima (recommended for more control)

```bash
# Install Colima
brew install colima docker

# Start Colima VM with enough resources for eBPF
colima start --cpu 2 --memory 4 --vm-type vz --vz-rosetta

# Verify kernel in Colima VM
colima ssh -- uname -r

# Build and run (same as above)
docker compose build
docker compose up -d
make test-traffic
```

### Mac troubleshooting

| Symptom | Fix |
|---|---|
| `CAP_BPF: unknown capability` | Remove `- BPF` from cap_add in docker-compose.yml (older Docker) |
| `mirror-agent` can't reach ewp-sim | Docker Desktop networking quirk — try `docker compose logs mirror-agent` |
| Build fails on Apple Silicon | The BPF compiler in Docker uses `x86_64-linux-gnu` headers. Add `--platform linux/amd64` to docker compose build if needed |

**Apple Silicon note:** The eBPF object is compiled for the BPF virtual machine (architecture-independent) — it works on both x86_64 and ARM64 Linux kernels. The Go binary, however, needs to be compiled for the host arch. The multi-stage Dockerfile handles this automatically.

---

## Option 3: Linux VM (production model — APIGEE on the same VM)

This is the production deployment path. The mirror-agent runs directly on the VM alongside APIGEE, with no containers.

### One-time setup

```bash
# On the APIGEE VM (Ubuntu 22.04 / RHEL 8+ / Debian 11+)

# 1. Verify kernel
uname -r    # need >= 5.8

# 2. Run the automated deployment script
#    This compiles the eBPF program, builds the Go binary, and installs a systemd service.
sudo bash scripts/deploy-on-vm.sh \
    --iface eth0 \
    --apigee-port 8080 \
    --ewp http://<ewp-host>:8081

# 3. Verify it's running
sudo systemctl status ebpf-mirror
sudo journalctl -fu ebpf-mirror
```

### Manual steps (if you prefer)

```bash
# Install deps
sudo apt-get install -y clang llvm libbpf-dev linux-headers-generic golang-go iproute2

# Compile BPF program
clang -O2 -g -target bpf \
    -I/usr/include/x86_64-linux-gnu \
    -c kernel/mirror.bpf.c -o mirror.bpf.o

# Build the agent
cd mirror-agent && go build -o mirror-agent .

# Mount BPF filesystem (if not already)
sudo mount -t bpf bpf /sys/fs/bpf

# Run the agent (needs root or CAP_NET_ADMIN + CAP_SYS_ADMIN + CAP_BPF)
sudo ./mirror-agent \
    -iface eth0 \
    -port 8080 \
    -ewp http://<ewp-host>:8081 \
    -bpf ./mirror.bpf.o
```

### Finding the right interface

```bash
# List interfaces
ip link show

# Find which interface carries traffic to APIGEE's port
ss -tlnp | grep 8080        # shows APIGEE's listening socket
ip route get <client-ip>    # shows which interface client traffic arrives on
```

### APIGEE-specific: multiple ports

If APIGEE listens on multiple ports (e.g. 8080 HTTP + 8443 HTTPS):

```bash
# Run a second agent instance for port 8443
sudo ./mirror-agent -iface eth0 -port 8443 -ewp http://<ewp-host>:8081 -bpf ./mirror.bpf.o &
```

### Validating the migration

```bash
# On any machine that can reach EWP:
curl http://<ewp-host>:8081/validation-report | python3 -m json.tool

# Expected output when ready to cut over:
# {
#   "summary": {
#     "total": 1247,
#     "passed": 1247,
#     "failed": 0,
#     "pass_rate": "100.0%",
#     "ready_to_cut": true      ← when this is true, EWP is handling all traffic correctly
#   }
# }
```

### Stopping the mirror (cutover complete)

```bash
# systemd
sudo systemctl stop ebpf-mirror
sudo systemctl disable ebpf-mirror

# Or if running manually:
kill $(pgrep mirror-agent)

# Verify no TC filters remain
sudo tc filter show dev eth0 ingress
```

---

## Verifying the eBPF hook is active

From any environment:

```bash
# Check TC filter is attached (run on the host/container that has the agent)
tc filter show dev eth0 ingress

# Expected output:
# filter protocol all pref 1 bpf chain 0
# filter protocol all pref 1 bpf chain 0 handle 0x1 mirror_ingress direct-action not_in_hw ...

# Check BPF maps (requires bpftool)
bpftool map list
bpftool map dump name cfg_target_port    # should show port 8080
bpftool map dump name counters           # shows matched packets + drops
```

---

## Quick smoke test sequence

```bash
# 1. Send a request directly to APIGEE
curl http://localhost:8080/api/v1/products

# 2. Immediately check if EWP received the mirrored copy
curl http://localhost:8081/validation-report

# 3. Check mirror-agent logs to confirm capture
docker compose logs mirror-agent | tail -20
# Look for: "assembled 1 request(s) from ..."
# Look for: "MIRROR OK  GET /api/v1/products  status=200  latency=..."

# 4. Check EWP logs to confirm it received the shadow request
docker compose logs ewp-sim | tail -20
# Look for: "[SHADOW] GET /api/v1/products from ..."
```

---

## Cutover decision criteria

| Metric | Green | Yellow | Red |
|---|---|---|---|
| EWP pass rate | ≥ 99% | 95–99% | < 95% |
| EWP latency vs APIGEE | ≤ 2× | 2–5× | > 5× |
| EWP error rate | 0% | < 1% | ≥ 1% |
| Sample size | ≥ 1000 requests | 100–999 | < 100 |

When all metrics are green for a sustained period (recommend 24h), it is safe to route live traffic to EWP and decommission the APIGEE path.
