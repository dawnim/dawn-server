package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/prismgo/horizon"
)

func TestHorizonStopIsScoped(t *testing.T) {
	ctx := context.Background()
	shared := horizon.NewMemoryStore(horizon.StoreOptions{})
	dir := t.TempDir()
	first := horizonStore{shared, filepath.Join(dir, "first.stop")}
	second := horizonStore{shared, filepath.Join(dir, "second.stop")}
	if err := first.RequestTerminate(ctx, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	a, err := first.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	global, err := shared.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.TerminateRequestedAt.IsZero() || !b.TerminateRequestedAt.IsZero() || !global.TerminateRequestedAt.IsZero() {
		t.Fatal("local stop leaked to another Horizon instance")
	}
	if err := first.ClearTerminateRequest(ctx); err != nil {
		t.Fatal(err)
	}
	a, err = first.Control(ctx)
	if err != nil || !a.TerminateRequestedAt.IsZero() {
		t.Fatal("local stop was not cleared", err)
	}
}
