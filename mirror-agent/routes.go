package main

import (
	"encoding/json"
	"log"
	"strings"
	"sync/atomic"
)

// Route maps an incoming APIGEE request path to an EWP destination path.
// Matching is prefix-based: if the request path starts with Apigee, the
// Apigee prefix is replaced with EWP and any remaining path is appended.
//
//	{"apigee": "/api/v1/", "ewp": "/v2/"}
//	  /api/v1/products → /v2/products
//	  /api/v1/orders   → /v2/orders
//
// To route to the same path, set both fields to the same value:
//
//	{"apigee": "/api/v1/products", "ewp": "/api/v1/products"}
type Route struct {
	Apigee string `json:"apigee"` // path prefix on the APIGEE side
	EWP    string `json:"ewp"`    // corresponding path prefix on the EWP side
}

// activeRoutes is replaced atomically on reload; nil means "mirror everything".
var activeRoutes atomic.Pointer[[]Route]

// loadRoutes parses raw as a JSON array of Routes and makes them active.
// An empty string disables filtering — every path is mirrored unchanged.
func loadRoutes(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		activeRoutes.Store(nil)
		log.Printf("routes: no filter configured — mirroring all paths to EWP")
		return
	}
	var routes []Route
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		log.Printf("routes: invalid JSON (%v) — keeping previous config", err)
		return
	}
	activeRoutes.Store(&routes)
	log.Printf("routes: %d rule(s) active (only these paths will be mirrored):", len(routes))
	for _, r := range routes {
		log.Printf("  APIGEE %-30s  →  EWP %s", r.Apigee, r.EWP)
	}
}

// resolveRoute returns the EWP path for a given APIGEE request path and
// whether the request should be forwarded at all.
//
// If no route table is loaded (nil), every path is forwarded unchanged.
// If a table is loaded, only paths matching a rule are forwarded; the rest
// are silently dropped.
func resolveRoute(apigeeePath string) (ewpPath string, forward bool) {
	routes := activeRoutes.Load()
	if routes == nil {
		return apigeeePath, true
	}
	for _, r := range *routes {
		if strings.HasPrefix(apigeeePath, r.Apigee) {
			remainder := apigeeePath[len(r.Apigee):]
			return r.EWP + remainder, true
		}
	}
	return "", false
}
