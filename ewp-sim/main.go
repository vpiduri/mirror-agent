// ewp-sim: simulates an EWP (Enterprise Web Proxy) backend.
// Designed to receive mirrored traffic from the eBPF mirror agent.
// It compares incoming requests against expected patterns and reports results
// so the migration team can evaluate readiness for traffic cutover.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	requestCount  atomic.Int64
	mirroredCount atomic.Int64
	successCount  atomic.Int64
	validationLog []ValidationEntry
	validationMu  sync.Mutex

	// readyThreshPct is the minimum per-endpoint pass rate (0–100) required
	// before ready_to_cut is reported as true. Defaults to 100 (all must pass).
	// Updated at runtime via PUT /config — no restart needed.
	threshMu       sync.RWMutex
	readyThreshPct float64 = 100.0
)

// ValidationEntry records one mirrored request and the EWP response outcome.
type ValidationEntry struct {
	Time            time.Time         `json:"time"`
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	StatusCode      int               `json:"status_code"`
	LatencyMs       int64             `json:"latency_ms"`
	Passed          bool              `json:"passed"`
	Note            string            `json:"note,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

func main() {
	port := flag.Int("port", 9091, "listen port")
	flag.Parse()

	logger := log.New(os.Stdout, "[EWP] ", log.LstdFlags)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler(logger))
	mux.HandleFunc("/api/v1/products", productsHandler(logger))
	mux.HandleFunc("/api/v1/orders", ordersHandler(logger))
	mux.HandleFunc("/api/v1/users/", usersHandler(logger))
	mux.HandleFunc("/metrics", metricsHandler(logger))
	mux.HandleFunc("/validation-report", validationReportHandler(logger))
	mux.HandleFunc("/config", configHandler(logger))
	mux.HandleFunc("/", echoHandler(logger))

	addr := fmt.Sprintf(":%d", *port)
	logger.Printf("EWP (Enterprise Web Proxy) simulator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}

func healthHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logRequest(logger, r)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":   "UP",
			"service":  "ewp-sim",
			"version":  "v2.0.0",
			"platform": "service-mesh",
			"time":     time.Now().UTC(),
		})
	}
}

func productsHandler(logger *log.Logger) http.HandlerFunc {
	products := []map[string]interface{}{
		{"id": "prod-001", "name": "API Gateway Standard", "price": 99.99, "category": "platform"},
		{"id": "prod-002", "name": "API Gateway Enterprise", "price": 299.99, "category": "platform"},
		{"id": "prod-003", "name": "Analytics Add-on", "price": 49.99, "category": "addon"},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		requestCount.Add(1)

		isMirrored := r.Header.Get("X-Mirror-Source") != ""
		if isMirrored {
			mirroredCount.Add(1)
		}

		var statusCode int
		var respBody map[string]interface{}

		switch r.Method {
		case http.MethodGet:
			statusCode = http.StatusOK
			respBody = map[string]interface{}{
				"products": products,
				"total":    len(products),
				"source":   "ewp",
			}
		case http.MethodPost:
			statusCode = http.StatusCreated
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			body["id"] = fmt.Sprintf("ewp-prod-%03d", requestCount.Load())
			body["source"] = "ewp"
			respBody = body
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		writeJSON(w, statusCode, respBody)
		record(logger, r, statusCode, time.Since(start), isMirrored, w.Header())
	}
}

func ordersHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		requestCount.Add(1)
		isMirrored := r.Header.Get("X-Mirror-Source") != ""
		if isMirrored {
			mirroredCount.Add(1)
		}

		var statusCode int
		var respBody map[string]interface{}

		switch r.Method {
		case http.MethodGet:
			statusCode = http.StatusOK
			respBody = map[string]interface{}{
				"orders": []map[string]interface{}{
					{"id": "ord-001", "status": "shipped", "amount": 99.99},
					{"id": "ord-002", "status": "pending", "amount": 149.99},
				},
				"source": "ewp",
			}
		case http.MethodPost:
			statusCode = http.StatusAccepted
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			body["orderId"] = fmt.Sprintf("ewp-ord-%03d", requestCount.Load())
			body["status"] = "accepted"
			body["source"] = "ewp"
			respBody = body
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		writeJSON(w, statusCode, respBody)
		record(logger, r, statusCode, time.Since(start), isMirrored, w.Header())
	}
}

func usersHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		requestCount.Add(1)
		isMirrored := r.Header.Get("X-Mirror-Source") != ""
		if isMirrored {
			mirroredCount.Add(1)
		}
		userID := r.URL.Path[len("/api/v1/users/"):]
		if userID == "" {
			userID = "unknown"
		}
		statusCode := http.StatusOK
		writeJSON(w, statusCode, map[string]interface{}{
			"userId":    userID,
			"name":      "Test User",
			"email":     "test@example.com",
			"plan":      "enterprise",
			"source":    "ewp",
			"platform":  "service-mesh",
			"timestamp": time.Now().UTC(),
		})
		record(logger, r, statusCode, time.Since(start), isMirrored, w.Header())
	}
}

func metricsHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total := requestCount.Load()
		mirrored := mirroredCount.Load()
		success := successCount.Load()

		var successRate float64
		if mirrored > 0 {
			successRate = float64(success) / float64(mirrored) * 100
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"total_requests":    total,
			"mirrored_requests": mirrored,
			"success_count":     success,
			"success_rate_pct":  fmt.Sprintf("%.1f%%", successRate),
			"service":           "ewp-sim",
		})
	}
}

// validationReportHandler returns the full log of mirrored requests and their outcomes,
// plus a per-path breakdown useful for identifying which endpoints need work.
func validationReportHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		validationMu.Lock()
		entries := make([]ValidationEntry, len(validationLog))
		copy(entries, validationLog)
		validationMu.Unlock()

		total := len(entries)
		passed := 0
		var totalLatencyMs int64

		type pathStats struct {
			Total           int               `json:"total"`
			Passed          int               `json:"passed"`
			Failed          int               `json:"failed"`
			PassRatePct     string            `json:"pass_rate"`
			AvgLatMs        int64             `json:"avg_latency_ms"`
			ReadyToCut      bool              `json:"ready_to_cut"`
			ResponseHeaders map[string]string `json:"response_headers_observed"`
			sumLatMs        int64
		}
		byPath := make(map[string]*pathStats)

		for _, e := range entries {
			if e.Passed {
				passed++
			}
			totalLatencyMs += e.LatencyMs

			ps := byPath[e.Method+" "+e.Path]
			if ps == nil {
				ps = &pathStats{ResponseHeaders: make(map[string]string)}
				byPath[e.Method+" "+e.Path] = ps
			}
			ps.Total++
			ps.sumLatMs += e.LatencyMs
			if e.Passed {
				ps.Passed++
			} else {
				ps.Failed++
			}
			// Accumulate latest value for each response header seen on this endpoint.
			for k, v := range e.ResponseHeaders {
				ps.ResponseHeaders[k] = v
			}
		}

		// Compute per-endpoint averages, pass rate, and ready_to_cut.
		threshMu.RLock()
		thresh := readyThreshPct
		threshMu.RUnlock()

		allEndpointsReady := len(byPath) > 0
		for _, ps := range byPath {
			if ps.Total > 0 {
				ps.AvgLatMs = ps.sumLatMs / int64(ps.Total)
				rate := safeDiv(ps.Passed, ps.Total) * 100
				ps.PassRatePct = fmt.Sprintf("%.1f%%", rate)
				ps.ReadyToCut = rate >= thresh
			}
			if !ps.ReadyToCut {
				allEndpointsReady = false
			}
		}

		var avgLatMs int64
		if total > 0 {
			avgLatMs = totalLatencyMs / int64(total)
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"summary": map[string]interface{}{
				"total":               total,
				"passed":              passed,
				"failed":              total - passed,
				"pass_rate":           fmt.Sprintf("%.1f%%", safeDiv(passed, total)*100),
				"avg_latency_ms":      avgLatMs,
				"ready_threshold_pct": fmt.Sprintf("%.1f%%", thresh),
				"ready_to_cut":        allEndpointsReady && total > 0,
			},
			"by_path": byPath,

			// comparison_scope describes exactly what this tool measures and
			// what it deliberately does not measure. Share this with any team
			// evaluating migration readiness.
			"comparison_scope": map[string]interface{}{
				"what_is_compared": []string{
					"HTTP response status code class (2xx / 4xx / 5xx) per mirrored request",
					"Response latency (per-endpoint average, ms)",
					"Endpoint availability — 404 from EWP means the route is not implemented yet",
					"Response headers returned by EWP (see response_headers_observed per endpoint)",
				},
				"what_is_not_compared": []string{
					"APIGEE response headers vs EWP response headers — the eBPF hook captures " +
						"client→APIGEE ingress only; APIGEE's outbound responses are not captured, " +
						"so a header-by-header diff is not possible without also adding an egress hook",
					"Response body content or JSON schema — EWP may return structurally different " +
						"payloads with the same status code; body comparison requires an egress hook " +
						"or a dedicated contract test suite",
					"Request transformations visible to the API provider (backend) app — " +
						"Envoy/EWP adds headers (x-forwarded-for, x-envoy-*, grpc-status, etc.) " +
						"before forwarding to the backend; the backend may behave differently " +
						"because of these additions, which this tool cannot detect",
					"Response header additions visible to the API consumer (client) app — " +
						"Envoy adds headers such as x-envoy-upstream-service-time, x-request-id, " +
						"server: envoy, and may modify or strip APIGEE-specific headers; client " +
						"applications that parse or validate response headers may break even when " +
						"the status code passes",
				},
				"migration_context": "This tool compares proxy-layer behavior between the " +
					"POD (Point-of-Departure) APIGEE proxy and the POA (Point-of-Arrival) EWP proxy. " +
					"A passing result confirms that EWP routes correctly and returns equivalent " +
					"HTTP status codes for the same traffic. It does not confirm that the API " +
					"provider application or API consumer application are unaffected by the migration. " +
					"Provider-layer and consumer-layer compatibility must be validated separately " +
					"(contract tests, integration tests, or canary traffic with client-side monitoring).",
			},

			"entries": entries,
		})
	}
}

// configHandler exposes runtime knobs for the EWP sim without requiring a restart.
//
//	GET  /config  — returns current settings
//	PUT  /config  — updates settings; body: {"ready_threshold_pct": 99.0}
//
// ready_threshold_pct (0–100): minimum per-endpoint pass rate for ready_to_cut.
// Changing it takes effect immediately on the next /validation-report call.
func configHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			threshMu.RLock()
			t := readyThreshPct
			threshMu.RUnlock()
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"ready_threshold_pct": t,
				"note":                "PUT /config with {\"ready_threshold_pct\": <0-100>} to update without restart",
			})

		case http.MethodPut, http.MethodPost:
			var body struct {
				ReadyThresholdPct float64 `json:"ready_threshold_pct"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if body.ReadyThresholdPct < 0 || body.ReadyThresholdPct > 100 {
				http.Error(w, "ready_threshold_pct must be between 0 and 100", http.StatusBadRequest)
				return
			}
			threshMu.Lock()
			readyThreshPct = body.ReadyThresholdPct
			threshMu.Unlock()
			logger.Printf("ready_threshold updated → %.1f%%", body.ReadyThresholdPct)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"ready_threshold_pct": body.ReadyThresholdPct,
				"status":              "updated",
			})

		default:
			http.Error(w, "use GET or PUT", http.StatusMethodNotAllowed)
		}
	}
}

func echoHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		isMirrored := r.Header.Get("X-Mirror-Source") != ""
		if isMirrored {
			mirroredCount.Add(1)
		}
		headers := make(map[string]string)
		for k, v := range r.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		statusCode := http.StatusOK
		writeJSON(w, statusCode, map[string]interface{}{
			"message":   "request received by EWP simulator",
			"method":    r.Method,
			"path":      r.URL.Path,
			"headers":   headers,
			"source":    "ewp",
			"is_mirror": isMirrored,
		})
		record(logger, r, statusCode, time.Since(start), isMirrored, w.Header())
	}
}

// record logs and stores the outcome of one mirrored request.
// respHeader is the http.Header from the ResponseWriter after writing — it
// captures every header EWP set, including any that Envoy would add in
// production (e.g. x-envoy-upstream-service-time, x-request-id, server).
func record(logger *log.Logger, r *http.Request, statusCode int, latency time.Duration, isMirrored bool, respHeader http.Header) {
	passed := statusCode >= 200 && statusCode < 300
	if passed && isMirrored {
		successCount.Add(1)
	}

	if isMirrored {
		note := ""
		if statusCode >= 500 {
			note = fmt.Sprintf("EWP internal error %d — EWP may not handle this endpoint yet", statusCode)
			logger.Printf("ERROR [SHADOW] %s %s -> %d (%s) — internal error, check EWP logs",
				r.Method, r.URL.Path, statusCode, latency.Round(time.Millisecond))
		} else if statusCode >= 400 {
			note = fmt.Sprintf("EWP returned %d — possible route or schema mismatch vs APIGEE", statusCode)
			logger.Printf("WARN  [SHADOW] %s %s -> %d (%s) — client error",
				r.Method, r.URL.Path, statusCode, latency.Round(time.Millisecond))
		}

		// Snapshot response headers (first value per name, lowercase).
		headers := make(map[string]string, len(respHeader))
		for k, vs := range respHeader {
			if len(vs) > 0 {
				headers[strings.ToLower(k)] = vs[0]
			}
		}

		entry := ValidationEntry{
			Time:            time.Now().UTC(),
			Method:          r.Method,
			Path:            r.URL.Path,
			StatusCode:      statusCode,
			LatencyMs:       latency.Milliseconds(),
			Passed:          passed,
			Note:            note,
			ResponseHeaders: headers,
		}
		validationMu.Lock()
		validationLog = append(validationLog, entry)
		if len(validationLog) > 1000 {
			validationLog = validationLog[len(validationLog)-1000:]
		}
		validationMu.Unlock()
	}
}

func logRequest(logger *log.Logger, r *http.Request) {
	isMirrored := r.Header.Get("X-Mirror-Source") != ""
	tag := ""
	if isMirrored {
		tag = " [SHADOW]"
	}
	logger.Printf("%s%s %s from %s", tag, r.Method, r.URL.Path, r.RemoteAddr)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Served-By", "ewp-sim")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck
}

func safeDiv(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
