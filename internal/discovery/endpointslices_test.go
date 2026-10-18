package discovery

import (
	"reflect"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
)

func ptr[T any](v T) *T { return &v }

func endpoint(ip string, ready bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses:  []string{ip},
		Conditions: discoveryv1.EndpointConditions{Ready: ptr(ready)},
	}
}

func TestReadyAddrs(t *testing.T) {
	slices := []*discoveryv1.EndpointSlice{
		{
			Ports:     []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(int32(8000))}},
			Endpoints: []discoveryv1.Endpoint{endpoint("10.0.0.2", true), endpoint("10.0.0.3", false)},
		},
		{
			Ports:     []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(int32(8000))}},
			Endpoints: []discoveryv1.Endpoint{endpoint("10.0.0.1", true), endpoint("10.0.0.2", true)},
		},
	}
	got := ReadyAddrs(slices, "http")
	want := []string{"http://10.0.0.1:8000", "http://10.0.0.2:8000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReadyAddrsWrongPortName(t *testing.T) {
	slices := []*discoveryv1.EndpointSlice{{
		Ports:     []discoveryv1.EndpointPort{{Name: ptr("metrics"), Port: ptr(int32(9090))}},
		Endpoints: []discoveryv1.Endpoint{endpoint("10.0.0.1", true)},
	}}
	if got := ReadyAddrs(slices, "http"); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
