// apigee-sim: simulates an APIGEE API gateway backend.
// Responds to requests with realistic API responses and logs every request so
// the test harness can verify both sides received the same traffic.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

var requestCount atomic.Int64

func main() {
	port := flag.Int("port", 9090, "listen port")
	flag.Parse()

	logger := log.New(os.Stdout, "[APIGEE] ", log.LstdFlags)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler(logger))
	mux.HandleFunc("/api/v1/products", productsHandler(logger))
	mux.HandleFunc("/api/v1/orders", ordersHandler(logger))
	mux.HandleFunc("/api/v1/users/", usersHandler(logger))
	mux.HandleFunc("/metrics", metricsHandler(logger))
	mux.HandleFunc("/", echoHandler(logger))

	addr := fmt.Sprintf(":%d", *port)
	logger.Printf("APIGEE simulator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}

func healthHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":  "UP",
			"service": "apigee-sim",
			"version": "v1.0.0",
			"time":    time.Now().UTC(),
		})
		logResponse(logger, r, http.StatusOK, time.Since(start))
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
		var status int
		switch r.Method {
		case http.MethodGet:
			status = http.StatusOK
			writeJSON(w, status, map[string]interface{}{
				"products": products,
				"total":    len(products),
				"source":   "apigee",
			})
		case http.MethodPost:
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			body["id"] = fmt.Sprintf("prod-%03d", requestCount.Load())
			body["source"] = "apigee"
			status = http.StatusCreated
			writeJSON(w, status, body)
		default:
			status = http.StatusMethodNotAllowed
			http.Error(w, "method not allowed", status)
		}
		logResponse(logger, r, status, time.Since(start))
	}
}

func ordersHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		requestCount.Add(1)
		var status int
		switch r.Method {
		case http.MethodGet:
			status = http.StatusOK
			writeJSON(w, status, map[string]interface{}{
				"orders": []map[string]interface{}{
					{"id": "ord-001", "status": "shipped", "amount": 99.99},
					{"id": "ord-002", "status": "pending", "amount": 149.99},
				},
				"source": "apigee",
			})
		case http.MethodPost:
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			body["orderId"] = fmt.Sprintf("ord-%03d", requestCount.Load())
			body["status"] = "accepted"
			body["source"] = "apigee"
			status = http.StatusAccepted
			writeJSON(w, status, body)
		default:
			status = http.StatusMethodNotAllowed
			http.Error(w, "method not allowed", status)
		}
		logResponse(logger, r, status, time.Since(start))
	}
}

func usersHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		userID := r.URL.Path[len("/api/v1/users/"):]
		if userID == "" {
			userID = "unknown"
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"userId":    userID,
			"name":      "Test User",
			"email":     "test@example.com",
			"plan":      "enterprise",
			"source":    "apigee",
			"timestamp": time.Now().UTC(),
		})
		logResponse(logger, r, http.StatusOK, time.Since(start))
	}
}

func metricsHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"total_requests": requestCount.Load(),
			"service":        "apigee-sim",
		})
	}
}

func echoHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logRequest(logger, r)
		headers := make(map[string]string)
		for k, v := range r.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"message": "request received by APIGEE simulator",
			"method":  r.Method,
			"path":    r.URL.Path,
			"query":   r.URL.RawQuery,
			"headers": headers,
			"source":  "apigee",
		})
		logResponse(logger, r, http.StatusOK, time.Since(start))
	}
}

func logRequest(logger *log.Logger, r *http.Request) {
	logger.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
}

func logResponse(logger *log.Logger, r *http.Request, status int, latency time.Duration) {
	logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, status, latency.Round(time.Millisecond))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Served-By", "apigee-sim")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck
}
