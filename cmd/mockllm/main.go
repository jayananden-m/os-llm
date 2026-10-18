// mockllm imitates a vLLM server: same port, same API, configurable speed.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	port := flag.String("port", "8000", "listen port")
	delay := flag.Duration("delay", 2*time.Second, "simulated generation time")
	flag.Parse()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(*delay):
		case <-r.Context().Done():
			return // client gave up
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "chat.completion",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "Hello from replica :" + *port},
				"finish_reason": "stop",
			}},
		})
	})

	log.Printf("mock vLLM listening on :%s (delay %s)", *port, *delay)
	log.Fatal(http.ListenAndServe(":"+*port, mux))
}
