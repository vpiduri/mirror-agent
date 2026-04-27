package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// streamKey uniquely identifies a TCP connection from the client side.
type streamKey struct {
	srcIP, dstIP     uint32
	srcPort, dstPort uint16
}

// stream buffers TCP payload segments until a full HTTP request is assembled.
type stream struct {
	buf      bytes.Buffer
	lastSeen time.Time
}

// CapturedRequest holds a fully reassembled HTTP request ready for mirroring.
type CapturedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
	SrcIP   string
}

// TCPTracker reassembles TCP segments into complete HTTP/1.x requests.
// It handles single-segment and multi-segment (large body) requests within a
// 10-second reassembly window before stale streams are discarded.
type TCPTracker struct {
	mu      sync.Mutex
	streams map[streamKey]*stream
}

func NewTCPTracker() *TCPTracker {
	t := &TCPTracker{streams: make(map[streamKey]*stream)}
	go t.reaper()
	return t
}

// Feed appends a packet segment to its TCP stream and returns any complete
// HTTP requests assembled so far.
func (t *TCPTracker) Feed(ev *PktEvent) []*CapturedRequest {
	agentStats.packetsReceived.Add(1)

	if ev.PayloadLen == 0 {
		return nil
	}

	data := ev.Payload[:ev.PayloadLen]

	if !looksLikeHTTP(data) && !looksLikeContinuation(data) {
		agentStats.packetsDropped.Add(1)
		return nil
	}

	key := streamKey{ev.SrcIP, ev.DstIP, ev.SrcPort, ev.DstPort}

	t.mu.Lock()
	s, ok := t.streams[key]
	if !ok {
		s = &stream{}
		t.streams[key] = s
	}
	s.lastSeen = time.Now()
	s.buf.Write(data)
	snapshot := make([]byte, s.buf.Len())
	copy(snapshot, s.buf.Bytes())
	t.mu.Unlock()

	reqs, consumed := parseHTTPRequests(snapshot)
	if len(reqs) > 0 {
		for i := range reqs {
			reqs[i].SrcIP = ipStr(ev.SrcIP)
		}
		agentStats.reqsAssembled.Add(int64(len(reqs)))
		t.mu.Lock()
		// Trim consumed bytes; delete if fully drained.
		if consumed >= len(snapshot) {
			delete(t.streams, key)
		} else {
			remaining := snapshot[consumed:]
			t.streams[key].buf.Reset()
			t.streams[key].buf.Write(remaining)
		}
		t.mu.Unlock()
		log.Printf("TCP assembled %d request(s) from %s:%d (buf=%d bytes)",
			len(reqs), ipStr(ev.SrcIP), ev.SrcPort, len(snapshot))
	}

	return reqs
}

// parseHTTPRequests extracts all complete HTTP/1.x requests from raw bytes.
// Returns (requests, total bytes consumed).
func parseHTTPRequests(data []byte) ([]*CapturedRequest, int) {
	var (
		results  []*CapturedRequest
		consumed int
	)
	for {
		req, n, err := parseOneRequest(data[consumed:])
		if err != nil {
			agentStats.parseErrors.Add(1)
			log.Printf("PARSE ERROR at offset %d: %v (first 64 bytes: %q)",
				consumed, err, truncate(data[consumed:], 64))
			break
		}
		if n == 0 {
			break // incomplete — waiting for more data
		}
		results = append(results, req)
		consumed += n
	}
	return results, consumed
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// parseOneRequest parses a single HTTP/1.x request from the front of data.
// Returns (request, bytes_consumed, error). Returns (nil, 0, nil) when the
// buffer is incomplete (more data needed).
func parseOneRequest(data []byte) (*CapturedRequest, int, error) {
	headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return nil, 0, nil // headers not fully received yet
	}

	headerSection := data[:headerEnd]
	lines := strings.Split(string(headerSection), "\r\n")
	if len(lines) == 0 {
		return nil, 0, fmt.Errorf("empty request")
	}

	// Request line: METHOD PATH HTTP/version
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 {
		return nil, 0, fmt.Errorf("malformed request line: %q", lines[0])
	}

	headers := make(http.Header)
	for _, line := range lines[1:] {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		headers.Add(strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]))
	}

	bodyStart := headerEnd + 4 // skip \r\n\r\n
	contentLength := 0
	if cl := headers.Get("Content-Length"); cl != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(cl)); err == nil {
			contentLength = v
		}
	}

	totalLen := bodyStart + contentLength
	if len(data) < totalLen {
		return nil, 0, nil // body not yet fully received
	}

	body := make([]byte, contentLength)
	copy(body, data[bodyStart:totalLen])

	return &CapturedRequest{
		Method:  parts[0],
		Path:    parts[1],
		Headers: headers,
		Body:    body,
	}, totalLen, nil
}

// reaper removes stale streams that have not received data in >10 seconds.
// A reaped stream means we received partial TCP data but never assembled a
// complete HTTP request — that request will NOT be mirrored to EWP.
func (t *TCPTracker) reaper() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Second)
		t.mu.Lock()
		for k, s := range t.streams {
			if s.lastSeen.Before(cutoff) {
				agentStats.streamsReaped.Add(1)
				log.Printf("WARN stream reaped (partial request lost): src=%s:%d dst=%s:%d buf=%d bytes age=%.1fs",
					ipStr(k.srcIP), k.srcPort,
					ipStr(k.dstIP), k.dstPort,
					s.buf.Len(),
					time.Since(s.lastSeen).Seconds())
				delete(t.streams, k)
			}
		}
		t.mu.Unlock()
	}
}

func looksLikeHTTP(data []byte) bool {
	for _, m := range []string{"GET ", "POST ", "PUT ", "DELETE ", "PATCH ", "HEAD ", "OPTIONS "} {
		if bytes.HasPrefix(data, []byte(m)) {
			return true
		}
	}
	return false
}

// looksLikeContinuation returns true for packets that are likely HTTP body
// continuations: mostly printable ASCII (heuristic, ≥80% of first 64 bytes).
func looksLikeContinuation(data []byte) bool {
	n := len(data)
	if n == 0 {
		return false
	}
	check := n
	if check > 64 {
		check = 64
	}
	printable := 0
	for _, b := range data[:check] {
		if (b >= 0x20 && b < 0x7f) || b == '\r' || b == '\n' || b == '\t' {
			printable++
		}
	}
	return printable*10 >= check*8
}
