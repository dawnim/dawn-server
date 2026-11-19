package cmd

import (
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/artisan"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	prismhttp "github.com/prismgo/framework/http"
	"github.com/prismgo/framework/support"
	"github.com/prismgo/horizon"
)

type StartCommand struct{}

func (*StartCommand) Definition() *console.Definition {
	return console.MustDefinition(i18n.Text("", "dawn:start {services=all : all、local或逗号分隔的服务名} {action=start : start、stop、restart、reload或status} {--json : 以JSON输出服务状态}"), i18n.Text("", "启动后台服务"))
}

func (*StartCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	selection, action := ctx.Input().Argument("services"), ctx.Input().Argument("action")
	display := supervisor.NewDisplay(ctx.IO(), i18n.Locale(ctx.Context()), ctx.Input().OptionBool("json"))
	if display.JSON && action == "start" {
		return i18n.Errorf("--json仅用于status、stop、restart或reload")
	}
	manager := os.Getenv("DAWN_LOCAL_MANAGER") == "1"
	_ = os.Unsetenv("DAWN_LOCAL_MANAGER")
	if manager {
		_ = os.Setenv("DAWN_DETACHED_MANAGER", "1")
	}
	names, err := supervisor.Parse(selection, action)
	if err != nil {
		return err
	}
	if config.GetBool("dawn.embedding.enabled", false) && (selection == "all" || selection == "local") {
		names = append(names, "embedding")
	}
	socket := support.BasePath(config.GetString("dawn.runtime_directory", "storage/framework"), "dawn.sock")
	if !manager && (selection != "local" || action != "start") {
		requested := selection
		if action == "start" && (selection == "all" || selection == "local") {
			requested = strings.Join(names, ",")
		}
		response, connected, err := supervisor.Control(ctx.Context(), socket, requested, action)
		if connected {
			display.Render("服务状态", response)
			return err
		}
		if action != "start" {
			return i18n.Errorf("本项目的服务管理器尚未运行")
		}
	}
	definitions, err := artisan.All()
	if err != nil {
		return err
	}
	hasHorizon := slices.ContainsFunc(definitions, func(d console.Definition) bool { return d.Name == "horizon" })
	if slices.Contains(names, "horizon") && !hasHorizon {
		return i18n.Errorf("请先注册Horizon服务提供者")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	server := prismhttp.CurrentServerConfig()
	wsAddr := config.GetString("dawn.ws_addr", ":8851")
	services := map[string]supervisor.Spec{}
	for _, name := range []string{"http", "ws", "cron", "horizon", "embedding"} {
		services[name] = supervisor.Spec{Command: []string{executable, "dawn:service", name}, Dir: support.BasePath(), Timeout: server.ShutdownTimeout}
	}
	httpSpec, wsSpec := services["http"], services["ws"]
	httpSpec.Address, wsSpec.Address = server.Addr(), wsAddr
	host, port, _ := net.SplitHostPort(server.Addr())
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	httpSpec.URL = "http://" + net.JoinHostPort(host, port)
	host, port, _ = net.SplitHostPort(wsAddr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	wsSpec.URL = "ws://" + net.JoinHostPort(host, port)
	httpSpec.Timeout, wsSpec.Timeout = 2*server.ShutdownTimeout+5*time.Second, 2*server.ShutdownTimeout+5*time.Second
	services["http"], services["ws"] = httpSpec, wsSpec
	horizonSpec := services["horizon"]
	horizonSpec.Timeout = 5 * time.Second
	if hasHorizon {
		for _, group := range horizon.Resolve().Config().Supervisors {
			horizonSpec.Timeout = max(horizonSpec.Timeout, time.Duration(group.Timeout)*time.Second+5*time.Second)
		}
	}
	services["horizon"] = horizonSpec
	services["frontend"] = supervisor.Spec{Command: []string{executable, "dawn:service", "frontend"}, Dir: support.BasePath(), Timeout: 10 * time.Second, URL: "http://localhost:" + strconv.Itoa(config.GetInt("dawn.local.port", 5173))}
	embeddingSpec := services["embedding"]
	embeddingSpec.Timeout = 15 * time.Second
	embeddingSpec.URL = "http://" + net.JoinHostPort(config.GetString("dawn.embedding.host", "127.0.0.1"), strconv.Itoa(config.GetInt("dawn.embedding.port", 8852)))
	services["embedding"] = embeddingSpec
	if selection == "local" && action == "start" && !manager {
		selected := make(map[string]supervisor.Spec, len(names))
		for _, name := range names {
			selected[name] = services[name]
		}
		return supervisor.Terminal(ctx.Context(), socket, supervisor.Spec{
			Command: []string{executable, "dawn:start", "local"}, Dir: support.BasePath(), Env: []string{"DAWN_LOCAL_MANAGER=1"},
		}, selected, display)
	}
	return supervisor.Run(ctx.Context(), socket, services, names, selection == "local" && config.GetBool("dawn.local.watch", true), display)
}
