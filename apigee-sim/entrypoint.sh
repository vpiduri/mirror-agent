#!/bin/bash
# Runs APIGEE simulator and the eBPF mirror agent as co-located processes,
# exactly as they would run on a production VM.
#
# Signal handling: SIGTERM/SIGINT stops both processes cleanly.
set -euo pipefail

APIGEE_PORT="${APIGEE_PORT:-9090}"
EWP_URL="${EWP_URL:-http://ewp-sim:9091}"
BPF_OBJ="${BPF_OBJ:-/ebpf/mirror.bpf.o}"
IFACE="${IFACE:-eth0}"

# Mount BPF filesystem if not already mounted (needed for eBPF map pinning).
if ! mount | grep -q "bpf on /sys/fs/bpf"; then
    mount -t bpf bpf /sys/fs/bpf 2>/dev/null || true
fi

echo "[init] Starting APIGEE simulator on :${APIGEE_PORT}"
/apigee-sim -port "${APIGEE_PORT}" &
APIGEE_PID=$!

# Wait for APIGEE to be ready before attaching the eBPF TC hook.
for i in $(seq 1 10); do
    if curl -sf "http://localhost:${APIGEE_PORT}/health" > /dev/null 2>&1; then
        echo "[init] APIGEE ready"
        break
    fi
    echo "[init] Waiting for APIGEE... (${i}/10)"
    sleep 1
done

echo "[init] Starting eBPF mirror agent on iface=${IFACE} port=${APIGEE_PORT} -> ${EWP_URL}"
/mirror-agent \
    -iface  "${IFACE}" \
    -port   "${APIGEE_PORT}" \
    -ewp    "${EWP_URL}" \
    -bpf    "${BPF_OBJ}" &
MIRROR_PID=$!

# Forward SIGTERM/SIGINT to both child processes.
_stop() {
    echo "[init] Shutting down (APIGEE pid=${APIGEE_PID}, mirror pid=${MIRROR_PID})"
    kill "${MIRROR_PID}" 2>/dev/null || true
    kill "${APIGEE_PID}" 2>/dev/null || true
    wait "${MIRROR_PID}" 2>/dev/null || true
    wait "${APIGEE_PID}" 2>/dev/null || true
    echo "[init] Both processes stopped"
}
trap _stop SIGTERM SIGINT

# If either process dies unexpectedly, stop the other and exit non-zero.
wait -n "${APIGEE_PID}" "${MIRROR_PID}"
EXITED=$?
echo "[init] A child process exited (status=${EXITED}) — stopping the other"
_stop
exit "${EXITED}"
