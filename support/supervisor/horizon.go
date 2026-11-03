package supervisor

import (
	"context"
	"os"
	"time"

	"github.com/prismgo/horizon"
)

// ConfigureHorizon 通过公开扩展接口限定本代服务的停机范围，队列和监控数据仍由原 Store 管理。
func ConfigureHorizon() {
	path := os.Getenv("DAWN_HORIZON_STOP_FILE")
	if path == "" {
		return
	}
	m := horizon.Resolve()
	horizon.WithProcessRunner(horizonRunner{})(m)
	horizon.WithStoreFactory(horizonStoreFactory{m.StoreFactory(), path})(m)
}

type horizonRunner struct{}

func (horizonRunner) Start(ctx context.Context, spec horizon.ProcessSpec) (horizon.ManagedProcess, error) {
	// 所有后代保留在 Dawn 创建的进程组中，master 崩溃后也能完整回收。
	spec.NewProcessGroup = false
	return (horizon.OSProcessRunner{}).Start(ctx, spec)
}

type horizonStoreFactory struct {
	horizon.StoreResolver
	path string
}

func (f horizonStoreFactory) ResolveStore(ctx context.Context, cfg horizon.Config) (horizon.Store, error) {
	store, err := f.StoreResolver.ResolveStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return horizonStore{store, f.path}, nil
}

type horizonStore struct {
	horizon.Store
	path string
}

func (s horizonStore) RequestTerminate(context.Context, time.Time, bool) error {
	return os.WriteFile(s.path, []byte("stop"), 0600)
}

func (s horizonStore) Control(ctx context.Context) (horizon.ControlState, error) {
	state, err := s.Store.Control(ctx)
	if err != nil {
		return state, err
	}
	if info, err := os.Stat(s.path); err == nil {
		state.TerminateRequestedAt, state.TerminateShouldWait = info.ModTime(), true
	} else if !os.IsNotExist(err) {
		return state, err
	}
	return state, nil
}

func (s horizonStore) ClearTerminateRequest(ctx context.Context) error {
	if err := os.Remove(s.path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.Store.ClearTerminateRequest(ctx)
}
