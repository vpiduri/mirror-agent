# eBPF Traffic Mirror — APIGEE → EWP

Mirrors live HTTP traffic from an APIGEE gateway to an Enterprise Web Proxy (EWP)
backend **without modifying APIGEE** and **without adding latency to the live path**.
The mirror is used to validate that EWP handles every request correctly before
cutting over production traffic.

```
  ┌──────────────────────── APIGEE VM (or apigee container) ───────────────────────┐
  │                                                                                  │
  │  eth0 ──► [TC ingress eBPF hook] ──► APIGEE process (:9090)  ← live path       │
  │                      │                                                           │
  │           (copy, zero extra latency)                                             │
  │                      │                                                           │
  │               mirror-agent (Go)                                                  │
  └──────────────────────┼───────────────────────────────────────────────────────────┘
                         │  async, fire-and-forget
                         ▼
             EWP — Enterprise Web Proxy (:9091)
                         │
             GET /validation-report  ← per-endpoint pass/fail + latency
```

Both the APIGEE process and the mirror agent run **on the same host** (VM or
container), matching the production topology exactly. The eBPF hook runs entirely
inside the kernel; the live path never touches Go code.

---

## How kernel and userspace communicate

This is the core of the system. Three BPF maps act as the shared-memory interface
between `kernel/mirror.bpf.c` (kernel) and `mirror-agent/` (Go userspace).

### 1. Ring buffer — kernel → Go (packet events)

```
kernel/mirror.bpf.c                         mirror-agent/loader.go
─────────────────────────────────────────   ──────────────────────────────────────
struct pkt_event {                          type PktEvent struct {
    __u32 src_ip;        // offset  0           SrcIP      uint32   // offset  0
    __u32 dst_ip;        // offset  4           DstIP      uint32   // offset  4
    __u16 src_port;      // offset  8           SrcPort    uint16   // offset  8
    __u16 dst_port;      // offset 10           DstPort    uint16   // offset 10
    __u16 payload_len;   // offset 12           PayloadLen uint16   // offset 12
    __u8  tcp_flags;     // offset 14           TCPFlags   uint8    // offset 14
    __u8  _pad;          // offset 15           // _pad skipped
    __u8  payload[1500]; // offset 16           Payload    [1500]byte // offset 16
};                                          }
```

For each TCP packet that matches the target port, the eBPF program:

1. Calls `bpf_ringbuf_reserve(&rb, sizeof(*ev), 0)` to claim a 1516-byte slot in
   the shared 8 MB ring buffer.
2. Fills the slot with IP addresses, ports, TCP flags, and up to 1500 bytes of
   payload via `bpf_skb_load_bytes()`.
3. Calls `bpf_ringbuf_submit(ev, 0)` — this is the atomic publish step. The kernel
   marks the slot as readable and wakes up the Go reader.

On the Go side, `ringbuf.Reader.Read()` blocks until a slot is available, then
returns the raw bytes. `loader.go:ReadEvents()` manually parses the fixed offsets
(`pktEventHeaderSize = 16`) into a `PktEvent` and passes it to the TCP tracker.

The ring buffer is **lock-free and zero-copy** between kernel and userspace — the
Go code reads directly from kernel-mapped memory.

### 2. Config map — Go → kernel (target port)

```
mirror-agent/loader.go                      kernel/mirror.bpf.c
──────────────────────────────────          ────────────────────────────────────
cfgMap.Put(uint32(0), targetPort)  ──►      __u16 *target =
                                              bpf_map_lookup_elem(
                                                &cfg_target_port, &k);
                                            if (bpf_ntohs(tcph->dest) != *target)
                                                return TC_ACT_OK; // skip packet
```

Before attaching the TC filter, the Go loader writes the target port into a
single-entry BPF array map. Every packet that reaches the eBPF hook reads this
value and discards the packet if its destination port doesn't match. This lets
the Go agent control filtering without recompiling the eBPF program.

### 3. Counters map — kernel → Go (statistics)

```
kernel/mirror.bpf.c                         mirror-agent/loader.go:PrintStats()
────────────────────────────────────        ────────────────────────────────────
counters[0]: matched packets                m.Lookup(uint32(0), &v)
counters[1]: ring buffer drops              m.Lookup(uint32(1), &v)

__sync_fetch_and_add(cnt, 1)  // atomic
```

The eBPF program atomically increments `counters[0]` on every matched packet and
`counters[1]` when a ring buffer slot cannot be reserved (consumer too slow). The
Go agent reads these at shutdown and on STATS log lines.

### TC attachment (how the hook gets wired in)

```
loader.go
  netlink.QdiscAdd(clsact qdisc)          // creates a clsact qdisc on eth0
  netlink.FilterAdd(BpfFilter{             // attaches mirror_ingress as TC filter
      Parent:       HANDLE_MIN_INGRESS,    //   ingress direction
      Fd:           prog.FD(),             //   BPF program file descriptor
      DirectAction: true,                  //   program verdict is final
  })
```

`clsact` is a special no-op qdisc that exists purely to host TC eBPF programs.
`DirectAction` means the program's return value (`TC_ACT_OK`) is used directly
as the traffic-control verdict — no further classifier chain.

### Full data-flow summary

```
packet arrives on eth0
    │
    ▼
[kernel TC ingress hook: mirror_ingress()]
    │  read cfg_target_port → check dest port
    │  mismatch → TC_ACT_OK (pass, no work done)
    │
    │  match → bpf_skb_load_bytes() → fill pkt_event
    │       → bpf_ringbuf_submit()  → wake Go reader
    │       → counters[0]++
    ▼
[APIGEE process — unaffected, normal response to client]

                ┌─────────────────────────────────┐
                │ Go: ringbuf.Reader.Read()        │
                │   → TCPTracker.Feed()            │  TCP reassembly
                │   → parseHTTPRequests()          │  HTTP parsing
                │   → resolveRoute()               │  path filter
                │   → HTTPMirror.Send()  ──────────┼──► EWP :9091
                └─────────────────────────────────┘
```

---

## Quick start

### Docker (WSL2 / Mac)

```bash
# 1. Check prerequisites (optional)
bash scripts/check-prereqs.sh

# 2. Build and start everything
make up

# Services:
#   apigee   → http://localhost:9090  (APIGEE simulator + eBPF mirror agent)
#   ewp-sim  → http://localhost:9091  (Enterprise Web Proxy simulator)
#   load-gen → sends traffic every 5 s automatically

# 3. Watch logs
make logs-apigee     # APIGEE + mirror agent (same container)
make logs-ewp        # EWP — shows [SHADOW] tag on mirrored requests

# 4. Send a burst of test requests
make test-traffic

# 5. Check the migration validation report
make report
```

### Linux VM (production)

```bash
# On the APIGEE VM (Ubuntu 22.04 / RHEL 8+ recommended)
sudo bash scripts/deploy-on-vm.sh \
    --iface      eth0 \
    --apigee-port 9090 \
    --ewp        http://<ewp-host>:9091

# Status
sudo systemctl status ebpf-mirror
sudo journalctl -fu ebpf-mirror
```

---

## Configuration

### Endpoint routing (which paths to mirror)

By default, **every** path is mirrored. To restrict to specific endpoints, set
`MIRROR_ROUTES` in `docker-compose.yml` (or pass `-routes` to the binary):

```yaml
# docker-compose.yml — apigee service → environment:
MIRROR_ROUTES: '[
  {"apigee": "/api/v1/products", "ewp": "/api/v1/products"},
  {"apigee": "/api/v1/orders",   "ewp": "/api/v1/orders"}
]'
```

**Rules:**
| Scenario | Config |
|---|---|
| Mirror same path | `{"apigee": "/api/v1/foo", "ewp": "/api/v1/foo"}` |
| Remap path prefix | `{"apigee": "/api/v1/", "ewp": "/v2/"}` |
| Mirror all (default) | `MIRROR_ROUTES: ""` (empty or omit) |

Matching is **prefix-based** — `"/api/v1/"` matches `/api/v1/products`,
`/api/v1/orders/123`, etc. Unmatched paths are silently dropped (counted as
`skipped` in STATS logs). Changing routes requires restarting the mirror agent
(APIGEE keeps serving uninterrupted).

### Port numbers

| Service | Default | Where to change |
|---|---|---|
| APIGEE / mirror-agent | `9090` | `APIGEE_PORT` env, `-port` flag |
| EWP | `9091` | `-port` flag, `EWP_URL` env |

---

## Observability

### Mirror agent logs (inside apigee container)

```
routes: 2 rule(s) active (only these paths will be mirrored):
  APIGEE "/api/v1/products"  →  EWP "/api/v1/products"
TC ingress hook attached on eth0 (ifindex 2)
eBPF attached — listening for HTTP on port 9090, mirroring to http://ewp-sim:9091

TCP assembled 1 request(s) from 172.28.0.3:51234 (buf=312 bytes)
MIRROR OK  GET /api/v1/products  status=200  latency=3ms  resp={...}
MIRROR WARN 4xx  POST /api/v1/orders  status=404  latency=2ms  ...
MIRROR ERROR 5xx  GET /api/v1/users/42  status=500  latency=12ms  ...

STATS  packets(recv=40 drop=12)  streams(reaped=0 parseErr=0 assembled=8)
       mirrors(skipped=4 sent=4 2xx=3 4xx=1 5xx=0 netErr=0)
```

STATS lines print every 30 seconds and on shutdown.

### EWP logs (ewp-sim container)

```
[EWP] GET /api/v1/products from 172.28.0.10:52001          # direct call
[EWP] [SHADOW] GET /api/v1/products from 172.28.0.10:52001 # mirrored copy
[EWP] WARN  [SHADOW] POST /api/v1/orders -> 404 (2ms) — client error
[EWP] ERROR [SHADOW] GET /api/v1/users/42 -> 500 (12ms) — internal error
```

### Validation report

```bash
curl http://localhost:9091/validation-report | python3 -m json.tool
```

```json
{
  "summary": {
    "total": 120,
    "passed": 118,
    "failed": 2,
    "pass_rate": "98.3%",
    "avg_latency_ms": 4,
    "ready_threshold_pct": "99.0%",
    "ready_to_cut": false
  },
  "by_path": {
    "GET /api/v1/products": {
      "total": 60, "passed": 60, "failed": 0,
      "pass_rate": "100.0%", "avg_latency_ms": 2, "ready_to_cut": true
    },
    "POST /api/v1/orders": {
      "total": 60, "passed": 58, "failed": 2,
      "pass_rate": "96.7%", "avg_latency_ms": 6, "ready_to_cut": false
    }
  }
}
```

`ready_to_cut` is `true` per-endpoint when its pass rate meets the threshold,
and `true` globally only when **every** endpoint is individually ready.

### Adjusting the ready_to_cut threshold (no restart)

```bash
# Lower to 99% (allows 1 failure per 100 requests)
curl -X PUT http://localhost:9091/config \
  -H "Content-Type: application/json" \
  -d '{"ready_threshold_pct": 99.0}'

# Check current threshold
curl http://localhost:9091/config
```

Takes effect immediately on the next `/validation-report` call. No data is reset.

---

## Cutover decision criteria

| Metric | Green | Yellow | Red |
|---|---|---|---|
| EWP pass rate | ≥ threshold | within 1% below | > 1% below |
| EWP avg latency | ≤ 2× APIGEE | 2–5× APIGEE | > 5× APIGEE |
| EWP 5xx rate | 0% | < 1% | ≥ 1% |
| Sample size | ≥ 1 000 requests | 100–999 | < 100 |

When `ready_to_cut: true` has held for a sustained period (recommend 24 h of
production traffic), it is safe to route live traffic directly to EWP and
decommission the mirror agent.

```bash
# After cutover — remove the TC filter from the interface
sudo systemctl stop ebpf-mirror
sudo tc filter show dev eth0 ingress   # verify clean
```

---

## Project structure

```
.
├── kernel/
│   └── mirror.bpf.c          eBPF TC program (C) — packet capture + ring buffer
│
├── mirror-agent/              Go userspace agent
│   ├── loader.go              loads BPF object, attaches TC hook, reads ring buffer
│   ├── tracker.go             TCP stream reassembly → HTTP request parsing
│   ├── routes.go              endpoint allowlist — resolveRoute() prefix matching
│   ├── mirror.go              HTTP replay to EWP (fire-and-forget goroutine)
│   ├── stats.go               atomic counters + periodic STATS log lines
│   └── main.go                flags, wiring, signal handling
│
├── apigee-sim/
│   ├── main.go                APIGEE simulator — /health /products /orders /users
│   ├── entrypoint.sh          starts both apigee-sim and mirror-agent as co-processes
│   └── Dockerfile             3-stage build: eBPF compile → Go build → runtime image
│
├── ewp-sim/
│   ├── main.go                EWP simulator — /validation-report /config /metrics
│   └── Dockerfile
│
├── scripts/
│   ├── deploy-on-vm.sh        one-shot deploy to a live APIGEE VM (systemd service)
│   └── check-prereqs.sh       pre-flight checks for Docker / VM paths
│
├── docker-compose.yml         local lab: apigee + ewp-sim + load-gen
├── Makefile                   build / up / down / logs / report / test-traffic
└── TESTING.md                 detailed testing guide (WSL2, Mac, Linux VM)
```

---

## Requirements

| Component | Minimum |
|---|---|
| Linux kernel | 5.8 (BPF ring buffer) |
| Docker | 20.10+ with `cap_add: [NET_ADMIN, SYS_ADMIN, BPF]` |
| Go | 1.21 (build only — not needed for Docker path) |
| clang/llvm | 14+ (build only — not needed for Docker path) |

The Docker path compiles everything inside the build stages — no local toolchain
is needed beyond Docker itself.

---

## BPF maps reference

| Map name | Type | Purpose |
|---|---|---|
| `rb` | `RINGBUF` (8 MB) | kernel → Go: one `pkt_event` per TCP segment |
| `cfg_target_port` | `ARRAY[1]` of `u16` | Go → kernel: which destination port to mirror |
| `counters` | `ARRAY[2]` of `u64` | kernel → Go: `[0]` matched packets, `[1]` ringbuf drops |

Inspect live maps inside the container:

```bash
docker exec -it apigee bash
tc filter show dev eth0 ingress        # confirm TC hook is attached
bpftool map list                        # list all BPF maps
bpftool map dump name cfg_target_port  # should show port 9090
bpftool map dump name counters         # matched packets + ring buffer drops
```
