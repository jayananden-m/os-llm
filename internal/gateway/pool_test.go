package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestPool(replicas, maxPer, maxQueue int) *Pool {
	reps := make([]*Replica, replicas)
	for i := range reps {
		reps[i] = &Replica{Addr: fmt.Sprintf("r%d", i)}
	}
	return newPool(reps, maxPer, maxQueue)
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
	p := newTestPool(2, 4, 0)
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
	p := newTestPool(1, 1, 1)
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
	p := newTestPool(1, 1, 1)
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
	p := newTestPool(1, 1, 1)
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
