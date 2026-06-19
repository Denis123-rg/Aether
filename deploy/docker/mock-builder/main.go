// Mock Flashbots-style builder for E2E tests.
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

var listenAndServe = http.ListenAndServe

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"bundleHash": "0xe2e"})
	})
	return mux
}

func main() {
	addr := ":18545"
	log.Printf("mock builder listening on %s", addr)
	if err := listenAndServe(addr, newHandler()); err != nil {
		log.Printf("server error: %v", err)
	}
}
