// Package registry keeps the gateway's set of models in sync with the
// ModelDeployment resources the operator manages.
package registry

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/jayananden-m/os-llm/internal/discovery"
	"github.com/jayananden-m/os-llm/internal/gateway"
)

// GVR identifies ModelDeployments. The gateway reads them as unstructured
// objects, so it does not import the operator's Go module.
var GVR = schema.GroupVersionResource{
	Group:    "serving.os-llm.dev",
	Version:  "v1alpha1",
	Resource: "modeldeployments",
}

// The operator names the model server's Service port "http".
const servicePort = "http"

type entry struct {
	model   string
	service string
	cancel  context.CancelFunc // stops this model's EndpointSlice watch
}

// Registry maps ModelDeployments (by resource name) to gateway models.
type Registry struct {
	ctx       context.Context
	gw        *gateway.Gateway
	kube      kubernetes.Interface
	namespace string
	log       *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry
}

func New(ctx context.Context, gw *gateway.Gateway, kube kubernetes.Interface, namespace string, log *slog.Logger) *Registry {
	return &Registry{ctx: ctx, gw: gw, kube: kube, namespace: namespace, log: log, entries: map[string]*entry{}}
}

// Start watches ModelDeployments and returns once the initial list is loaded.
func (r *Registry) Start(dyn dynamic.Interface) error {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, r.namespace, nil)
	inf := factory.ForResource(GVR).Informer()

	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.onUpsert(obj) },
		UpdateFunc: func(_, obj any) { r.onUpsert(obj) },
		DeleteFunc: func(obj any) { r.onDelete(obj) },
	}); err != nil {
		return err
	}

	factory.Start(r.ctx.Done())
	if !cache.WaitForCacheSync(r.ctx.Done(), inf.HasSynced) {
		return errors.New("modeldeployment cache did not sync")
	}
	return nil
}

func (r *Registry) onUpsert(obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	model, _, _ := unstructured.NestedString(u.Object, "spec", "model")
	maxPer, _, _ := unstructured.NestedInt64(u.Object, "spec", "maxInFlightPerReplica")
	maxQueue, _, _ := unstructured.NestedInt64(u.Object, "spec", "maxQueue")
	service, _, _ := unstructured.NestedString(u.Object, "status", "serviceName")
	r.Upsert(u.GetName(), model, service, int(maxPer), int(maxQueue))
}

func (r *Registry) onDelete(obj any) {
	// A missed delete arrives wrapped in a tombstone.
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		r.Remove(u.GetName())
	}
}

// Upsert applies one ModelDeployment's current state. Service is empty until the
// operator has reconciled it, in which case the model is served but has no replicas yet.
func (r *Registry) Upsert(name, model, service string, maxPerReplica, maxQueue int) {
	if model == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.entries[name]
	if e != nil && e.model != model { // spec.model was renamed
		r.removeLocked(name)
		e = nil
	}
	pool := r.gw.UpsertModel(model, maxPerReplica, maxQueue)
	if e == nil {
		e = &entry{model: model}
		r.entries[name] = e
		r.log.Info("model added", "modeldeployment", name, "model", model)
	}

	if service == "" || service == e.service || r.kube == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	ctx, cancel := context.WithCancel(r.ctx)
	e.service, e.cancel = service, cancel

	target := discovery.Target{
		Namespace: r.namespace,
		Service:   service,
		PortName:  servicePort,
		OnChange: func(addrs []string) {
			added, removed, err := pool.SetReplicas(addrs)
			if err != nil {
				r.log.Error("update replicas", "model", model, "err", err)
			}
			if added > 0 || removed > 0 {
				r.log.Info("replicas updated", "model", model, "added", added, "removed", removed, "replicas", addrs)
			}
		},
	}
	go func() {
		if err := discovery.Watch(ctx, r.kube, target, r.log); err != nil && ctx.Err() == nil {
			r.log.Error("watch replicas", "model", model, "service", service, "err", err)
		}
	}()
}

// Remove stops serving the model behind a deleted ModelDeployment.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(name)
}

func (r *Registry) removeLocked(name string) {
	e := r.entries[name]
	if e == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	r.gw.RemoveModel(e.model)
	delete(r.entries, name)
	r.log.Info("model removed", "modeldeployment", name, "model", e.model)
}
