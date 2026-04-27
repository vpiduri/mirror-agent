#!/usr/bin/env bash
# deploy-on-vm.sh
#
# Installs and runs the eBPF mirror agent on a Linux VM where APIGEE is running.
# Run this script ON the APIGEE VM as root (or with sudo).
#
# Usage:
#   sudo bash deploy-on-vm.sh \
#       --iface eth0 \
#       --apigee-port 9090 \
#       --ewp http://ewp-host:9091
#
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
IFACE="eth0"
APIGEE_PORT="9090"
EWP_URL=""
INSTALL_DIR="/opt/ebpf-mirror"
BPF_OBJ="$INSTALL_DIR/mirror.bpf.o"
AGENT_BIN="$INSTALL_DIR/mirror-agent"
SERVICE_NAME="ebpf-mirror"

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --iface)        IFACE="$2"; shift 2 ;;
        --apigee-port)  APIGEE_PORT="$2"; shift 2 ;;
        --ewp)          EWP_URL="$2"; shift 2 ;;
        --install-dir)  INSTALL_DIR="$2"; shift 2 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

if [[ -z "$EWP_URL" ]]; then
    echo "ERROR: --ewp <url> is required (e.g. --ewp http://ewp-host:9091)"
    exit 1
fi

echo "======================================================================"
echo "  eBPF Traffic Mirror — VM Deployment"
echo "  Interface  : $IFACE"
echo "  APIGEE port: $APIGEE_PORT"
echo "  EWP target : $EWP_URL"
echo "  Install dir: $INSTALL_DIR"
echo "======================================================================"
echo ""

# ── Check kernel version ──────────────────────────────────────────────────────
KVER=$(uname -r)
KMAJ=$(echo "$KVER" | cut -d. -f1)
KMIN=$(echo "$KVER" | cut -d. -f2)
echo "Kernel: $KVER"
if [[ "$KMAJ" -lt 5 ]] || ([[ "$KMAJ" -eq 5 ]] && [[ "$KMIN" -lt 8 ]]); then
    echo "ERROR: Kernel >= 5.8 required for BPF ring buffer. Found: $KVER"
    exit 1
fi
echo "✓ Kernel version OK"

# ── Install build deps ────────────────────────────────────────────────────────
echo ""
echo "Installing build dependencies..."
if command -v apt-get &>/dev/null; then
    apt-get update -qq
    apt-get install -y --no-install-recommends \
        clang llvm libbpf-dev linux-headers-generic linux-libc-dev \
        golang-go iproute2 curl
elif command -v yum &>/dev/null; then
    yum install -y clang llvm libbpf-devel kernel-headers golang iproute
elif command -v dnf &>/dev/null; then
    dnf install -y clang llvm libbpf-devel kernel-devel golang iproute
else
    echo "WARNING: Unknown package manager — install clang, libbpf-dev, golang manually"
fi
echo "✓ Dependencies installed"

# ── Compile ───────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

echo ""
echo "Compiling eBPF program..."
TRIPLE=$(uname -m)-linux-gnu
[ "$(uname -m)" = "x86_64" ]  && TRIPLE=x86_64-linux-gnu
[ "$(uname -m)" = "aarch64" ] && TRIPLE=aarch64-linux-gnu
rm -rf /usr/include/asm && ln -s /usr/include/${TRIPLE}/asm /usr/include/asm
clang -O2 -g -target bpf \
    -I/usr/include/${TRIPLE} \
    -I/usr/include \
    -c "$REPO_ROOT/kernel/mirror.bpf.c" \
    -o "$BPF_OBJ"
echo "✓ Compiled: $BPF_OBJ"

echo ""
echo "Building mirror-agent..."
cd "$REPO_ROOT/mirror-agent"
go build -o "$AGENT_BIN" .
echo "✓ Built: $AGENT_BIN"

# ── Mount BPF filesystem if needed ────────────────────────────────────────────
if ! mount | grep -q "bpf on /sys/fs/bpf"; then
    echo ""
    echo "Mounting BPF filesystem..."
    mount -t bpf bpf /sys/fs/bpf
    echo "bpf /sys/fs/bpf bpf defaults 0 0" >> /etc/fstab
    echo "✓ BPF filesystem mounted"
fi

# ── Install systemd service ───────────────────────────────────────────────────
echo ""
echo "Installing systemd service: $SERVICE_NAME ..."

cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=eBPF Traffic Mirror Agent (APIGEE → EWP)
After=network.target
# Ensure this starts AFTER the APIGEE process so the interface is up.
# Adjust the After= line to match your APIGEE service name.

[Service]
Type=simple
ExecStart=$AGENT_BIN \\
    -iface=$IFACE \\
    -port=$APIGEE_PORT \\
    -ewp=$EWP_URL \\
    -bpf=$BPF_OBJ
Restart=on-failure
RestartSec=5
# eBPF requires these capabilities.
AmbientCapabilities=CAP_NET_ADMIN CAP_SYS_ADMIN CAP_NET_RAW CAP_BPF
CapabilityBoundingSet=CAP_NET_ADMIN CAP_SYS_ADMIN CAP_NET_RAW CAP_BPF
NoNewPrivileges=false
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"

echo ""
echo "======================================================================"
echo "  Deployment complete."
echo ""
echo "  Service status : systemctl status $SERVICE_NAME"
echo "  Live logs      : journalctl -fu $SERVICE_NAME"
echo "  Stop mirroring : systemctl stop $SERVICE_NAME"
echo "  Remove         : systemctl disable --now $SERVICE_NAME"
echo ""
echo "  EWP validation report:"
echo "    curl $EWP_URL/validation-report | python3 -m json.tool"
echo "======================================================================"
