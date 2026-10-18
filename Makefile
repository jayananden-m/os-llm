CLUSTER ?= os-llm
TAG     ?= dev

.PHONY: test images kind-up kind-down load monitoring observability deploy up forward grafana jaeger

test:
	go vet ./... && go test -race ./...

images:
	docker build --build-arg APP=gateway -t os-llm/gateway:$(TAG) .
	docker build --build-arg APP=mockllm -t os-llm/mockllm:$(TAG) .

kind-up:
	kind create cluster --config deploy/kind/cluster.yaml

kind-down:
	kind delete cluster --name $(CLUSTER)

load: images
	kind load docker-image os-llm/gateway:$(TAG) os-llm/mockllm:$(TAG) --name $(CLUSTER)

monitoring:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update
	helm upgrade --install kube-prom prometheus-community/kube-prometheus-stack \
		-n monitoring --create-namespace -f deploy/monitoring/values.yaml --wait
	kubectl apply -f deploy/monitoring/dashboard.yaml

observability:
	kubectl apply -f deploy/k8s/namespace.yaml
	kubectl apply -f deploy/observability/

deploy: load
	kubectl apply -f deploy/k8s/namespace.yaml
	kubectl apply -f deploy/k8s/
	kubectl apply -f deploy/monitoring/servicemonitor.yaml
	kubectl -n os-llm rollout status statefulset/mock-qwen --timeout=2m
	kubectl -n os-llm rollout status deploy/gateway --timeout=2m

up: kind-up monitoring observability deploy

forward:
	kubectl -n os-llm port-forward svc/gateway 8080:8080

grafana:
	kubectl -n monitoring port-forward svc/kube-prom-grafana 3000:80

jaeger:
	kubectl -n observability port-forward svc/jaeger 16686:16686
