// Package discovery keeps a model's replica list in sync with the ready
// endpoints of a Kubernetes Service.
package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strconv"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Target is the Service to watch and what to do when its ready endpoints change.
type Target struct {
	Namespace string
	Service   string
	PortName  string // empty takes the first port
	OnChange  func(addrs []string)
}

// NewClient uses the in-cluster service account, falling back to the local kubeconfig.
func NewClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

// Watch starts an informer on the Service's EndpointSlices, calls OnChange once
// after the initial sync and again on every change. It returns once synced;
// the informer keeps running until ctx is cancelled.
func Watch(ctx context.Context, client kubernetes.Interface, t Target, log *slog.Logger) error {
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithNamespace(t.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.LabelSelector = discoveryv1.LabelServiceName + "=" + t.Service
		}),
	)
	slices := factory.Discovery().V1().EndpointSlices()
	lister := slices.Lister()

	update := func() {
		list, err := lister.List(labels.Everything())
		if err != nil {
			log.Error("list endpointslices", "service", t.Service, "err", err)
			return
		}
		t.OnChange(ReadyAddrs(list, t.PortName))
	}

	if _, err := slices.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { update() },
		UpdateFunc: func(any, any) { update() },
		DeleteFunc: func(any) { update() },
	}); err != nil {
		return err
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), slices.Informer().HasSynced) {
		return errors.New("endpointslice cache did not sync")
	}
	update()
	return nil
}

// ReadyAddrs returns sorted http:// addresses of ready endpoints across all slices.
// Terminating and unready pods are excluded, so they stop receiving new requests.
func ReadyAddrs(slices []*discoveryv1.EndpointSlice, portName string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range slices {
		port := int32(-1)
		for _, p := range s.Ports {
			if p.Port == nil {
				continue
			}
			if portName == "" || (p.Name != nil && *p.Name == portName) {
				port = *p.Port
				break
			}
		}
		if port < 0 {
			continue
		}
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && !*e.Conditions.Ready {
				continue
			}
			if len(e.Addresses) == 0 {
				continue
			}
			addr := "http://" + net.JoinHostPort(e.Addresses[0], strconv.Itoa(int(port)))
			if !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	sort.Strings(out)
	return out
}
