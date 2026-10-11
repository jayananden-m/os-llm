package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	mux := http.NewServeMux() // Router: Empty Router

	// (pattern, func)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":       "qwen2.5-0.5b-instruct",
					"object":   "model",
					"owned_by": "os",
				},
			},
		})
	})

	backend, err := url.Parse("http://localhost:8000")
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	mux.Handle("POST /v1/chat/completions", proxy)

	log.Println("gateway listening on port 8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
