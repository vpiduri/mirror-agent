#!/usr/bin/env bash
# Verifies the environment is ready to run the eBPF traffic mirror.
# Two paths are checked separately:
#   Docker path (WSL2 / Mac) — clang/llvm are NOT needed on the host;
#                              they run inside the Docker build stage.
#   VM path                  — clang/llvm/go ARE needed on the host.
set -euo pipefail

OK="\033[0;32m✓\033[0m"
FAIL="\033[0;31m✗\033[0m"
WARN="\033[0;33m!\033[0m"
INFO="\033[0;34m·\033[0m"

check() {
    local label="$1" cmd="$2"
    if eval "$cmd" &>/dev/null; then
        echo -e " $OK  $label"
        return 0
    else
        echo -e " $FAIL  $label"
        return 1
    fi
}

optional() {
    local label="$1" cmd="$2" note="$3"
    if eval "$cmd" &>/dev/null; then
        echo -e " $OK  $label"
    else
        echo -e " $INFO  $label — not found ($note)"
    fi
}

warn() { echo -e " $WARN  $1"; }

echo ""
echo "=== eBPF Traffic Mirror — Prerequisites Check ==="
echo ""

# ── Kernel version ────────────────────────────────────────────────────────────
KVER=$(uname -r)
echo "  Kernel: $KVER"
KMAJ=$(echo "$KVER" | cut -d. -f1)
KMIN=$(echo "$KVER" | cut -d. -f2)
if [ "$KMAJ" -gt 5 ] || ([ "$KMAJ" -eq 5 ] && [ "$KMIN" -ge 8 ]); then
    echo -e " $OK  Kernel >= 5.8 (BPF ring buffer supported)"
else
    echo -e " $FAIL  Kernel < 5.8 — BPF ring buffer not available. Upgrade required."
fi

# ── Kernel features ───────────────────────────────────────────────────────────
echo ""
echo "── Kernel features ──────────────────────────────────────────────────────"
check  "BTF available (/sys/kernel/btf/vmlinux)" "ls /sys/kernel/btf/vmlinux"
check  "BPF syscall enabled"  "ls /proc/sys/kernel/unprivileged_bpf_disabled"

# /sys/fs/bpf empty is normal on WSL2 — container mounts its own bpffs.
if mount | grep -q "bpf on /sys/fs/bpf"; then
    echo -e " $OK  BPF filesystem mounted on host"
else
    echo -e " $INFO  /sys/fs/bpf not mounted on host — this is fine for the Docker path."
    echo -e "       The container mounts its own bpffs internally."
fi

# ── Docker (required for WSL2 / Mac path) ────────────────────────────────────
echo ""
echo "── Docker (required for WSL2 / Mac) ────────────────────────────────────"
check "docker"         "command -v docker"
check "docker compose" "docker compose version"
if command -v docker &>/dev/null; then
    if docker info &>/dev/null; then
        echo -e " $OK  Docker daemon reachable"
    else
        echo -e " $FAIL  Docker daemon not running — start Docker Desktop"
    fi
fi

# ── Host toolchain (only needed for direct VM deployment, not Docker) ─────────
echo ""
echo "── Host toolchain (VM deployment only — NOT needed for Docker path) ────"
optional "clang"   "command -v clang"   "compiled inside Docker build stage instead"
optional "llvm"    "command -v llc"     "compiled inside Docker build stage instead"
optional "go"      "command -v go"      "built inside Docker build stage instead"
optional "bpftool" "command -v bpftool" "optional — useful for inspecting live BPF maps"

# ── WSL2-specific ─────────────────────────────────────────────────────────────
if uname -r | grep -qi microsoft; then
    echo ""
    echo "── WSL2 notes ───────────────────────────────────────────────────────────"
    echo -e " $INFO  WSL2 kernel detected."
    echo -e " $INFO  Docker Desktop for Windows uses the WSL2 kernel — eBPF works out of the box."
    echo -e " $INFO  /sys/fs/bpf being empty on the host is expected and not a problem."
    if command -v docker &>/dev/null && docker info &>/dev/null; then
        echo -e " $OK  Docker Desktop is running and reachable from WSL2."
    else
        warn "Docker Desktop does not appear to be running. Start it from Windows first."
    fi
fi

echo ""
echo "── Summary ──────────────────────────────────────────────────────────────"
echo "  To run with Docker (WSL2 / Mac):   make build && make up"
echo "  To deploy on a VM (production):    sudo bash scripts/deploy-on-vm.sh --ewp <url>"
echo ""
