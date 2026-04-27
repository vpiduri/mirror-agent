#!/usr/bin/env bash
# Verifies that the environment supports eBPF TC programs.
set -euo pipefail

OK="\033[0;32m✓\033[0m"
FAIL="\033[0;31m✗\033[0m"
WARN="\033[0;33m!\033[0m"

check() {
    local label="$1"
    local cmd="$2"
    if eval "$cmd" &>/dev/null; then
        echo -e " $OK  $label"
        return 0
    else
        echo -e " $FAIL  $label"
        return 1
    fi
}

warn() {
    echo -e " $WARN  $1"
}

echo ""
echo "=== eBPF Traffic Mirror — Prerequisites Check ==="
echo ""

# Kernel version
KVER=$(uname -r)
echo "  Kernel: $KVER"
KMAJ=$(echo "$KVER" | cut -d. -f1)
KMIN=$(echo "$KVER" | cut -d. -f2)
if [ "$KMAJ" -gt 5 ] || ([ "$KMAJ" -eq 5 ] && [ "$KMIN" -ge 8 ]); then
    echo -e " $OK  Kernel >= 5.8 (ring buffer supported)"
else
    echo -e " $FAIL  Kernel < 5.8 — ring buffer not available. Upgrade required."
fi

echo ""
echo "── Toolchain ────────────────────────────────────────────────────────────"
check "clang"     "command -v clang"
check "llvm"      "command -v llc"
check "bpftool"   "command -v bpftool"
check "docker"    "command -v docker"
check "docker compose" "docker compose version"

echo ""
echo "── Kernel features ──────────────────────────────────────────────────────"
check "BPF syscall enabled"     "ls /proc/sys/kernel/unprivileged_bpf_disabled"
check "BTF available"           "ls /sys/kernel/btf/vmlinux"
check "eBPF fs mounted"         "mount | grep -q bpf"
check "TC clsact qdisc support" "tc qdisc help 2>&1 | grep -q clsact || true"

echo ""
echo "── Capabilities ─────────────────────────────────────────────────────────"
if [ "$(id -u)" -eq 0 ]; then
    echo -e " $OK  Running as root"
else
    warn "Not running as root — eBPF loading and TC attachment require CAP_NET_ADMIN + CAP_SYS_ADMIN"
    warn "Docker containers will use these caps via cap_add in docker-compose.yml"
fi

echo ""
echo "── BTF vmlinux.h (optional, for CO-RE builds) ───────────────────────────"
if command -v bpftool &>/dev/null && ls /sys/kernel/btf/vmlinux &>/dev/null; then
    echo "  To generate vmlinux.h for your kernel:"
    echo "    bpftool btf dump file /sys/kernel/btf/vmlinux format c > kernel/vmlinux.h"
fi

echo ""
echo "── WSL2-specific notes ──────────────────────────────────────────────────"
if uname -r | grep -qi microsoft; then
    warn "WSL2 kernel detected. Notes:"
    warn "  • Docker Desktop for Windows shares the WSL2 kernel — eBPF works."
    warn "  • If using docker-ce inside WSL2, run: sudo dockerd &"
    warn "  • The BPF filesystem is usually auto-mounted; if not:"
    warn "    sudo mount -t bpf bpf /sys/fs/bpf"
fi

echo ""
echo "Done. Resolve any ✗ items above before running 'make up'."
echo ""
