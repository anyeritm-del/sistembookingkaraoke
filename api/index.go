// Package handler is the single Vercel Go function. vercel.json rewrites
// every /api/* request here, and httpapi routes it.
package handler

import (
	"context"
	"log"
	"net/http"
	"sync"

	"karaoke/pkg/httpapi"
)

var (
	once    sync.Once
	server  http.Handler
	initErr error
)

// Handler is the entry point that Vercel calls. The server is built once per
// instance, so warm requests reuse the Sheets client and its read cache.
func Handler(w http.ResponseWriter, r *http.Request) {
	once.Do(func() {
		server, initErr = httpapi.NewFromEnv(context.Background())
		if initErr != nil {
			log.Printf("init: %v", initErr)
		}
	})
	if initErr != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"konfigurasi server belum lengkap, cek environment variable di Vercel"}`))
		return
	}
	server.ServeHTTP(w, r)
}
