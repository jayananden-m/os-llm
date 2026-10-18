CLUSTER ?= os-llm
TAG     ?= dev

.PHONY: proto cli test images kind-up kind-down load monitoring observability operator deploy up forward grafana jaeger

# Regenerates Go code from proto/. Needs protoc, protoc-gen-go and protoc-gen-go-grpc.
proto:
	mkdir -p gen
	protoc -I proto \
		--go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
		proto/osllm/v1/admin.proto

cli:
	go build -o bin/osllm ./cmd/osllm

test:
	go vet ./... && go test -race ./...
	cd operator && $(MAKE) test

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

# Builds the operator image, loads it into kind, installs the CRD and runs the controller in-cluster.
operator:
	cd operator && $(MAKE) docker-build IMG=os-llm/operator:$(TAG)
	kind load docker-image os-llm/operator:$(TAG) --name $(CLUSTER)
	cd operator && $(MAKE) deploy IMG=os-llm/operator:$(TAG)
	kubectl -n operator-system rollout status deploy/operator-controller-manager --timeout=2m

deploy: load
	kubectl apply -f deploy/k8s/namespace.yaml
	kubectl apply -f deploy/k8s/
	kubectl apply -f deploy/monitoring/servicemonitor.yaml
	kubectl apply -f deploy/models/
	kubectl -n os-llm rollout status deploy/gateway --timeout=2m

up: kind-up monitoring observability operator deploy

forward:
	kubectl -n os-llm port-forward svc/gateway 8080:8080 9090:9090

grafana:
	kubectl -n monitoring port-forward svc/kube-prom-grafana 3000:80

jaeger:
	kubectl -n observability port-forward svc/jaeger 16686:16686
