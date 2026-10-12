package main

import (
	"bytes"
	"encoding/json"
	"io"
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

	// backend, err := url.Parse("http://localhost:8000")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// proxy := httputil.NewSingleHostReverseProxy(backend)
	// mux.Handle("POST /v1/chat/completions", proxy)

	backends := map[string]string{
		"qwen2.5-0.5b-instruct": "http://localhost:8000",
	}

	proxies := map[string]*httputil.ReverseProxy{}
	for name, addr := range backends {
		u, err := url.Parse(addr)
		if err != nil {
			log.Fatal(err)
		}
		proxies[name] = httputil.NewSingleHostReverseProxy(u)
	}
	
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read body")
			return
		}

		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
			return
		}

		proxy, ok := proxies[req.Model]
		if !ok {
			writeError(w, http.StatusNotFound, "model_not_found", "unknown model: "+ req.Model)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	})



	log.Println("gateway listening on port 8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}


func writeError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": errType},
	})
}
