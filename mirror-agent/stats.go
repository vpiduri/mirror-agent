package main

import (
	"log"
	"sync/atomic"
	"time"
)

// agentStats holds all counters for the mirror agent's lifetime.
// All fields are updated atomically across goroutines.
var agentStats struct {
	// eBPF / packet layer
	packetsReceived atomic.Int64 // ring buffer events read
	packetsDropped  atomic.Int64 // non-HTTP, filtered before TCP tracker

	// TCP reassembly
	streamsReaped  atomic.Int64 // partial streams discarded by reaper (lost requests)
	parseErrors    atomic.Int64 // malformed HTTP in assembled buffer
	reqsAssembled  atomic.Int64 // complete HTTP requests handed to mirror

	// Mirror outcomes
	mirrorsSkipped atomic.Int64 // path not in route allowlist — not forwarded
	mirrorsSent    atomic.Int64 // total attempts to EWP
	mirrors2xx     atomic.Int64 // EWP returned 2xx
	mirrors4xx     atomic.Int64 // EWP returned 4xx (route/format mismatch)
	mirrors5xx     atomic.Int64 // EWP returned 5xx (EWP internal error)
	mirrorsError   atomic.Int64 // network / timeout error reaching EWP
}

// startStatsPrinter logs a stats summary every interval.
// Run in a goroutine; returns when the process exits.
func startStatsPrinter(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		printStats()
	}
}

func printStats() {
	recv := agentStats.packetsReceived.Load()
	drop := agentStats.packetsDropped.Load()
	reap := agentStats.streamsReaped.Load()
	perr := agentStats.parseErrors.Load()
	assm := agentStats.reqsAssembled.Load()
	skip := agentStats.mirrorsSkipped.Load()
	sent := agentStats.mirrorsSent.Load()
	ok2  := agentStats.mirrors2xx.Load()
	e4   := agentStats.mirrors4xx.Load()
	e5   := agentStats.mirrors5xx.Load()
	nErr := agentStats.mirrorsError.Load()

	log.Printf("STATS  packets(recv=%d drop=%d)  streams(reaped=%d parseErr=%d assembled=%d)  mirrors(skipped=%d sent=%d 2xx=%d 4xx=%d 5xx=%d netErr=%d)",
		recv, drop, reap, perr, assm, skip, sent, ok2, e4, e5, nErr)
}
