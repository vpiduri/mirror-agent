package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// MirrorResult holds the outcome of one shadow request to EWP.
type MirrorResult struct {
	Method     string
	Path       string
	StatusCode int
	Body       []byte
	Latency    time.Duration
	Error      error
}

// HTTPMirror replays captured HTTP requests to the EWP shadow backend.
type HTTPMirror struct {
	ewpBase string
	client  *http.Client
}

func NewHTTPMirror(ewpBase string) *HTTPMirror {
	return &HTTPMirror{
		ewpBase: strings.TrimRight(ewpBase, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send replays a captured request to EWP and logs the result.
// Designed to be called in a goroutine — it does not return results to the caller.
func (m *HTTPMirror) Send(req *CapturedRequest) {
	agentStats.mirrorsSent.Add(1)
	result := m.doSend(req)
	m.logResult(req, result)
}

func (m *HTTPMirror) doSend(captured *CapturedRequest) MirrorResult {
	start := time.Now()

	url := m.ewpBase + captured.Path

	var bodyReader io.Reader
	if len(captured.Body) > 0 {
		bodyReader = bytes.NewReader(captured.Body)
	}

	httpReq, err := http.NewRequest(captured.Method, url, bodyReader)
	if err != nil {
		return MirrorResult{
			Method: captured.Method,
			Path:   captured.Path,
			Error:  err,
		}
	}

	// Copy headers from captured request, excluding hop-by-hop headers.
	for k, vals := range captured.Headers {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	// Mark the request as mirrored so EWP can identify it.
	httpReq.Header.Set("X-Mirror-Source", "ebpf-mirror-agent")
	httpReq.Header.Set("X-Original-Client", captured.SrcIP)

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return MirrorResult{
			Method:  captured.Method,
			Path:    captured.Path,
			Latency: time.Since(start),
			Error:   err,
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // cap at 64KB for logging

	return MirrorResult{
		Method:     captured.Method,
		Path:       captured.Path,
		StatusCode: resp.StatusCode,
		Body:       body,
		Latency:    time.Since(start),
	}
}

func (m *HTTPMirror) logResult(req *CapturedRequest, r MirrorResult) {
	// Network / timeout error — EWP was unreachable.
	if r.Error != nil {
		agentStats.mirrorsError.Add(1)
		log.Printf("MIRROR NET-ERROR  %s %s  latency=%s  err=%v  req_body=%s",
			r.Method, r.Path,
			r.Latency.Round(time.Millisecond),
			r.Error,
			bodySnippet(req.Body))
		return
	}

	switch {
	case r.StatusCode >= 200 && r.StatusCode < 300:
		agentStats.mirrors2xx.Add(1)
		log.Printf("MIRROR OK  %s %s  status=%d  latency=%s  resp=%s",
			r.Method, r.Path, r.StatusCode,
			r.Latency.Round(time.Millisecond),
			bodySnippet(r.Body))

	case r.StatusCode >= 400 && r.StatusCode < 500:
		// 4xx: EWP rejected the request — likely a route or schema mismatch vs APIGEE.
		agentStats.mirrors4xx.Add(1)
		log.Printf("MIRROR WARN 4xx  %s %s  status=%d  latency=%s  req_body=%s  resp=%s",
			r.Method, r.Path, r.StatusCode,
			r.Latency.Round(time.Millisecond),
			bodySnippet(req.Body),
			bodySnippet(r.Body))

	case r.StatusCode >= 500:
		// 5xx: EWP had an internal error — higher severity than a 4xx mismatch.
		agentStats.mirrors5xx.Add(1)
		log.Printf("MIRROR ERROR 5xx  %s %s  status=%d  latency=%s  req_body=%s  resp=%s",
			r.Method, r.Path, r.StatusCode,
			r.Latency.Round(time.Millisecond),
			bodySnippet(req.Body),
			bodySnippet(r.Body))

	default:
		// 1xx / 3xx — unexpected for a backend API.
		log.Printf("MIRROR UNEXPECTED  %s %s  status=%d  latency=%s",
			r.Method, r.Path, r.StatusCode, r.Latency.Round(time.Millisecond))
	}
}

// bodySnippet returns a compact, safe-to-log representation of a body.
func bodySnippet(b []byte) string {
	if len(b) == 0 {
		return "(empty)"
	}
	s := string(b)
	if json.Valid(b) {
		var compact bytes.Buffer
		if json.Compact(&compact, b) == nil {
			s = compact.String()
		}
	}
	if len(s) > 256 {
		return s[:256] + "…"
	}
	return s
}

// isHopByHop reports whether a header name is a hop-by-hop header that must
// not be forwarded in a mirrored request.
func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}
