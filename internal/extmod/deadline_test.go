package extmod

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// slowCore answers reads only when the caller gives up.
type slowCore struct{ sdk.Core }

func (slowCore) Read(ctx context.Context, _, _ string, _ map[string]string) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCallsIntoTheCoreKeepTheModulesDeadline(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	defer stop()
	m := &Module{core: slowCore{}, life: life}
	start := time.Now()
	_, err := m.serve("read", json.RawMessage(`{"module":"apps","read":"instances","timeout_ms":20}`))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("read with a deadline: %v after %v", err, time.Since(start))
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := withTimeout(context.Background(), 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("no timeout_ms gave a deadline")
	}
	ctx, cancel2 := withTimeout(context.Background(), 1000)
	defer cancel2()
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > time.Second {
		t.Fatalf("deadline %v %v", d, ok)
	}
}
