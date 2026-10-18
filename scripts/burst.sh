#!/usr/bin/env bash
# Fires N concurrent requests and prints one status code per line.
N=${1:-12}
for i in $(seq 1 "$N"); do
  curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"qwen2.5-0.5b-instruct","messages":[{"role":"user","content":"hi"}]}' &
done
wait
