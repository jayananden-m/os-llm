package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func bareReplica(addr string) (*Replica, error) { return &Replica{Addr: addr}, nil }

func newTestPool(t *testing.T, replicas, maxPer, maxQueue int) *Pool {
	t.Helper()
	p := newPool(maxPer, maxQueue, bareReplica)
	addrs := make([]string, replicas)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("r%d", i)
	}
	if _, _, err := p.SetReplicas(addrs); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitQueued(t *testing.T, p *Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, q := p.Stats(); q == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue never reached %d", n)
}

func TestLeastBusy(t *testing.T) {
	p := newTestPool(t, 2, 4, 0)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := p.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := p.replicas[0].inFlight, p.replicas[1].inFlight; a != 2 || b != 1 {
		t.Fatalf("in-flight = %d,%d, want 2,1", a, b)
	}
}

func TestQueueFull(t *testing.T) {
	p := newTestPool(t, 1, 1, 1)
	ctx := context.Background()
	if _, err := p.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	go p.Acquire(ctx) // takes the one queue spot
	waitQueued(t, p, 1)

	if _, err := p.Acquire(ctx); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
}

func TestReleaseHandsSlotToWaiter(t *testing.T) {
	p := newTestPool(t, 1, 1, 1)
	ctx := context.Background()
	first, _ := p.Acquire(ctx)

	got := make(chan *Replica)
	go func() {
		r, _ := p.Acquire(ctx)
		got <- r
	}()
	waitQueued(t, p, 1)

	p.Release(first)
	if r := <-got; r != first {
		t.Fatal("waiter did not receive the freed replica")
	}
	if in, q := p.Stats(); in != 1 || q != 0 {
		t.Fatalf("stats = %d in-flight, %d queued, want 1, 0", in, q)
	}
}

func TestQueueTimeout(t *testing.T) {
	p := newTestPool(t, 1, 1, 1)
	if _, err := p.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := p.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if _, q := p.Stats(); q != 0 {
		t.Fatalf("queued = %d after timeout, want 0", q)
	}
}

func TestScaleUpServesQueuedRequest(t *testing.T) {
	p := newTestPool(t, 1, 1, 1)
	ctx := context.Background()
	if _, err := p.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan *Replica)
	go func() {
		r, _ := p.Acquire(ctx)
		got <- r
	}()
	waitQueued(t, p, 1)

	added, removed, err := p.SetReplicas([]string{"r0", "r1"})
	if err != nil || added != 1 || removed != 0 {
		t.Fatalf("SetReplicas = %d added, %d removed, %v", added, removed, err)
	}
	if r := <-got; r.Addr != "r1" {
		t.Fatalf("waiter got %s, want the new replica r1", r.Addr)
	}
	if r0 := p.replicas[0]; r0.inFlight != 1 {
		t.Fatalf("r0 in-flight = %d, want 1 (kept across update)", r0.inFlight)
	}
}

func TestScaleDownDrainsRemovedReplica(t *testing.T) {
	p := newTestPool(t, 2, 1, 0)
	ctx := context.Background()
	r0, _ := p.Acquire(ctx)

	if _, removed, _ := p.SetReplicas([]string{"r1"}); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	// New work only lands on r1.
	r, err := p.Acquire(ctx)
	if err != nil || r.Addr != "r1" {
		t.Fatalf("acquire after scale-down = %v, %v", r, err)
	}
	// The request still running on the removed replica finishes cleanly.
	p.Release(r0)
	if in, _ := p.Stats(); in != 1 {
		t.Fatalf("in-flight = %d, want 1", in)
	}
}

func TestEmptyPoolQueues(t *testing.T) {
	p := newPool(1, 1, bareReplica) // no replicas yet, as before discovery syncs
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
