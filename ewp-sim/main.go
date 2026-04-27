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
	requestCount   atomic.Int64
	mirroredCount  atomic.Int64
	successCount   atomic.Int64
	validationLog  []ValidationEntry
	validationMu   sync.Mutex
)

// ValidationEntry records whether an EWP response matched APIGEE expectations.
type ValidationEntry struct {
	Time       time.Time     `json:"time"`
	Method     string        `json:"method"`
	Path       string        `json:"path"`
	StatusCode int           `json:"status_code"`
	LatencyMs  int64         `json:"latency_ms"`
	Passed     bool          `json:"passed"`
	Note       string        `json:"note,omitempty"`
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

		record(logger, r, statusCode, time.Since(start), isMirrored)
		writeJSON(w, statusCode, respBody)
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

		record(logger, r, statusCode, time.Since(start), isMirrored)
		writeJSON(w, statusCode, respBody)
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
		record(logger, r, statusCode, time.Since(start), isMirrored)
		writeJSON(w, statusCode, map[string]interface{}{
			"userId":    userID,
			"name":      "Test User",
			"email":     "test@example.com",
			"plan":      "enterprise",
			"source":    "ewp",
			"platform":  "service-mesh",
			"timestamp": time.Now().UTC(),
		})
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
			Total     int   `json:"total"`
			Passed    int   `json:"passed"`
			Failed    int   `json:"failed"`
			AvgLatMs  int64 `json:"avg_latency_ms"`
			sumLatMs  int64
		}
		byPath := make(map[string]*pathStats)

		for _, e := range entries {
			if e.Passed {
				passed++
			}
			totalLatencyMs += e.LatencyMs

			ps := byPath[e.Method+" "+e.Path]
			if ps == nil {
				ps = &pathStats{}
				byPath[e.Method+" "+e.Path] = ps
			}
			ps.Total++
			ps.sumLatMs += e.LatencyMs
			if e.Passed {
				ps.Passed++
			} else {
				ps.Failed++
			}
		}

		// Compute averages.
		for _, ps := range byPath {
			if ps.Total > 0 {
				ps.AvgLatMs = ps.sumLatMs / int64(ps.Total)
			}
		}

		var avgLatMs int64
		if total > 0 {
			avgLatMs = totalLatencyMs / int64(total)
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"summary": map[string]interface{}{
				"total":          total,
				"passed":         passed,
				"failed":         total - passed,
				"pass_rate":      fmt.Sprintf("%.1f%%", safeDiv(passed, total)*100),
				"avg_latency_ms": avgLatMs,
				"ready_to_cut":   passed == total && total > 0,
			},
			"by_path": byPath,
			"entries": entries,
		})
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
		record(logger, r, statusCode, time.Since(start), isMirrored)
		writeJSON(w, statusCode, map[string]interface{}{
			"message":    "request received by EWP simulator",
			"method":     r.Method,
			"path":       r.URL.Path,
			"headers":    headers,
			"source":     "ewp",
			"is_mirror":  isMirrored,
		})
	}
}

func record(logger *log.Logger, r *http.Request, statusCode int, latency time.Duration, isMirrored bool) {
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

		entry := ValidationEntry{
			Time:       time.Now().UTC(),
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: statusCode,
			LatencyMs:  latency.Milliseconds(),
			Passed:     passed,
			Note:       note,
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
