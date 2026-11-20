package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

//go:embed index.html styles.css app.js
var assets embed.FS

func main() {
	port := flag.Int("port", 8090, "port dashboard pada 127.0.0.1")
	apiPort := flag.Int("api-port", 8080, "port backend pada 127.0.0.1")
	flag.Parse()
	if *port < 1 || *port > 65535 || *apiPort < 1 || *apiPort > 65535 || *port == *apiPort {
		log.Fatal("gunakan dua port yang berbeda antara 1 dan 65535")
	}

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", *apiPort)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":"Dashboard tidak bisa menghubungi backend. Periksa Docker Compose dan port API."}`)
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/health/", proxy)
	mux.Handle("/api/v1/", proxy)
	mux.Handle("/", http.FileServerFS(assets))

	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			mux.ServeHTTP(w, r)
		}),
	}

	log.Printf("Dashboard http://%s  |  Backend %s", server.Addr, target)
	log.Fatal(server.ListenAndServe())
}
