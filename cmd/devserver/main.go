// Command devserver runs the app locally: static files from ./public and the
// API from httpapi, the same as on Vercel.
//
//	STORE=memory ADMIN_PIN=123456 SESSION_SECRET=... TV_KEY=... go run ./cmd/devserver
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

	"karaoke/pkg/httpapi"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	dir := flag.String("public", "public", "static files directory")
	flag.Parse()

	api, err := httpapi.NewFromEnv(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	static := http.FileServer(http.Dir(*dir))
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Match Vercel's cleanUrls: /tv serves tv.html.
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path, ".") {
			r.URL.Path += ".html"
		}
		static.ServeHTTP(w, r)
	})
	log.Printf("listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
