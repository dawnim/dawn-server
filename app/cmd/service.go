package cmd

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	prismhttp "github.com/prismgo/framework/http"
	"github.com/prismgo/framework/support"
	"github.com/prismgo/horizon"
)

// ServiceCommand 是管理器派生子进程的执行入口，不递归启动管理器。
type ServiceCommand struct{}

func (*ServiceCommand) Definition() *console.Definition {
	d := console.MustDefinition("dawn:service {service}", "Dawn service process")
	d.Hidden = true
	return d
}

func (*ServiceCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	name := ctx.Input().Argument("service")
	if name == "frontend" {
		port := strconv.Itoa(config.GetInt("dawn.local.port", 5173))
		server := prismhttp.CurrentServerConfig()
		httpHost := cmp.Or(server.Host, "127.0.0.1")
		if httpHost == "0.0.0.0" || httpHost == "::" {
			httpHost = "127.0.0.1"
		}
		wsHost, wsPort, err := net.SplitHostPort(config.GetString("dawn.ws_addr", ":8851"))
		if err != nil {
			return err
		}
		if wsHost == "" || wsHost == "0.0.0.0" || wsHost == "::" {
			wsHost = "127.0.0.1"
		}
		return supervisor.Frontend(ctx.Context(), supervisor.Spec{
			Command: []string{"pnpm", "run", "dev", "--host", "127.0.0.1", "--port", port, "--strictPort", "--force"},
			Dir:     support.BasePath(config.GetString("dawn.local.directory", "admin")), Probe: net.JoinHostPort("127.0.0.1", port), Timeout: 5 * time.Second,
			Env: []string{"DAWN_HTTP_TARGET=http://" + net.JoinHostPort(httpHost, server.Port), "DAWN_WS_TARGET=ws://" + net.JoinHostPort(wsHost, wsPort),
				"DAWN_ADMIN_PREFIX=" + config.GetString("dawn.admin_prefix", "/admin"), "DAWN_WS_PATH=" + config.GetString("dawn.ws_path", "/")},
		})
	}
	if name == "horizon" {
		pollCtx, cancel := context.WithCancel(ctx.Context())
		pollDone := make(chan struct{})
		defer func() { cancel(); <-pollDone }()
		go func() {
			defer close(pollDone)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					store, err := horizon.Resolve().ResolveStore(pollCtx)
					if err != nil {
						continue
					}
					masters, err := store.Masters(pollCtx, time.Now())
					if err != nil {
						continue
					}
					for _, master := range masters {
						if master.PID != os.Getpid() {
							continue
						}
						supervisors, err := store.Supervisors(pollCtx, time.Now())
						if err != nil {
							continue
						}
						count := 0
						for _, s := range supervisors {
							if s.MasterID == master.ID {
								count++
							}
						}
						if count == master.SupervisorCount {
							_ = supervisor.Ready()
							return
						}
					}
				}
			}
		}()
		return ctx.Call("horizon")
	}
	if name == "cron" {
		if err := supervisor.Ready(); err != nil {
			return err
		}
		return ctx.Call("cron")
	}
	if name != "http" && name != "ws" {
		return i18n.Errorf("未知服务: %s", name)
	}
	a := admin.Resolve()
	if err := a.Redis.Ping(ctx.Context()).Err(); err != nil {
		return err
	}
	configuration := prismhttp.CurrentServerConfig()
	var server *http.Server
	var err error
	if name == "http" {
		// 停机先等待在途请求，不能先取消它们的基础 context。
		server, err = a.Runtime.NewHTTPServer(context.WithoutCancel(ctx.Context()), configuration.Port)
		if err != nil {
			return err
		}
	} else {
		server = prismhttp.NewServerWithConfig(a.Realtime, configuration)
		server.Addr = a.Options.WSAddr
	}
	listener, err := prismhttp.InheritedListener()
	if err != nil {
		return err
	}
	if listener == nil {
		listener, err = net.Listen("tcp", server.Addr)
	}
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx.Context())
	defer cancel()
	var readyErr error
	err = prismhttp.ListenAndServeGracefulContext(runCtx, server, configuration.ShutdownTimeout,
		prismhttp.WithListener(listener), prismhttp.WithStartedHook(func() {
			readyErr = supervisor.Ready()
			if readyErr != nil {
				cancel()
			}
		}))
	if name == "ws" {
		drain, done := context.WithTimeout(context.Background(), configuration.ShutdownTimeout)
		defer done()
		a.Realtime.Drain(drain)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, readyErr)
}
