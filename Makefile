.PHONY: all build up down logs logs-mirror logs-ewp clean \
        test-traffic report prereqs \
        deploy-vm

# ── Docker Compose lifecycle ──────────────────────────────────────────────────
# The eBPF C code is compiled inside the Docker build stage.
# No local clang is required for the Docker path.

build:
	docker compose build

up:
	docker compose up -d --build
	@echo ""
	@echo "Services running:"
	@echo "  apigee   → http://localhost:9090  (APIGEE sim + eBPF mirror agent, same container)"
	@echo "  ewp-sim  → http://localhost:9091  (Enterprise Web Proxy simulator)"
	@echo "  load-gen → sending traffic every 5s"
	@echo ""
	@echo "  make logs          — follow all logs"
	@echo "  make logs-apigee   — APIGEE + mirror agent logs"
	@echo "  make test-traffic  — send a burst of test requests"
	@echo "  make report        — EWP migration validation report"

down:
	docker compose down

logs:
	docker compose logs -f --tail=60

logs-apigee:
	docker compose logs -f apigee

logs-ewp:
	docker compose logs -f ewp-sim

# ── Checks ────────────────────────────────────────────────────────────────────
prereqs:
	bash scripts/check-prereqs.sh

# ── Smoke tests ───────────────────────────────────────────────────────────────
test-traffic:
	@echo "=== Sending test requests to APIGEE (port 9090) ==="
	@echo "(the eBPF agent running inside the same container will mirror each to EWP)"
	@echo ""
	@echo "--- GET /health ---"
	curl -s http://localhost:9090/health | python3 -m json.tool
	@echo ""
	@echo "--- GET /api/v1/products ---"
	curl -s http://localhost:9090/api/v1/products | python3 -m json.tool
	@echo ""
	@echo "--- POST /api/v1/orders ---"
	curl -s -X POST http://localhost:9090/api/v1/orders \
		-H "Content-Type: application/json" \
		-d '{"customerId":"cust-1","items":[{"sku":"prod-001","qty":1}]}' \
		| python3 -m json.tool
	@echo ""
	@echo "--- GET /api/v1/users/user-42 ---"
	curl -s http://localhost:9090/api/v1/users/user-42 | python3 -m json.tool
	@echo ""
	@echo "Now check: make report"

# ── EWP migration validation report ──────────────────────────────────────────
report:
	@echo "=== EWP (Enterprise Web Proxy) Migration Validation Report ==="
	@curl -s http://localhost:9091/validation-report | python3 -m json.tool
	@echo ""
	@echo "=== EWP Metrics ==="
	@curl -s http://localhost:9091/metrics | python3 -m json.tool
	@echo ""
	@echo "When 'ready_to_cut' is true, EWP is handling all traffic correctly."

# ── Production VM deployment ─────────────────────────────────────────────────
# Run this on the APIGEE VM itself. Set EWP_URL before calling.
# Example:
#   make deploy-vm EWP_URL=http://ewp-prod-host:8081 IFACE=ens3 APIGEE_PORT=8080
EWP_URL  ?= http://ewp-host:9091
IFACE    ?= eth0
APIGEE_PORT ?= 9090

deploy-vm:
	@test -n "$(EWP_URL)" || (echo "ERROR: set EWP_URL=http://your-ewp-host:port"; exit 1)
	sudo bash scripts/deploy-on-vm.sh \
		--iface $(IFACE) \
		--apigee-port $(APIGEE_PORT) \
		--ewp $(EWP_URL)

# ── Cleanup ───────────────────────────────────────────────────────────────────
clean:
	docker compose down --rmi local --volumes
