package cmd

import (
	"encoding/json"
	"os"
	"slices"
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
	return console.MustDefinition(i18n.Text("", "dawn:start {services=all : all、local或逗号分隔的服务名} {action=start : start、stop、restart、reload或status}"), i18n.Text("", "启动后台服务"))
}

func (*StartCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	selection, action := ctx.Input().Argument("services"), ctx.Input().Argument("action")
	names, err := supervisor.Parse(selection, action)
	if err != nil {
		return err
	}
	socket := support.BasePath(config.GetString("dawn.runtime_directory", "storage/framework"), "dawn.sock")
	response, connected, err := supervisor.Control(ctx.Context(), socket, selection, action)
	if connected {
		if err == nil {
			data, _ := json.MarshalIndent(response, "", "  ")
			ctx.IO().Info(string(data))
		}
		return err
	}
	if action != "start" {
		return i18n.Errorf("本项目的服务管理器尚未运行")
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
	for _, name := range []string{"http", "ws", "cron", "horizon"} {
		services[name] = supervisor.Spec{Command: []string{executable, "dawn:service", name}, Dir: support.BasePath(), Timeout: server.ShutdownTimeout}
	}
	httpSpec, wsSpec := services["http"], services["ws"]
	httpSpec.Address, wsSpec.Address = server.Addr(), wsAddr
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
	services["frontend"] = supervisor.Spec{Command: []string{executable, "dawn:service", "frontend"}, Dir: support.BasePath(), Timeout: 10 * time.Second}
	return supervisor.Run(ctx.Context(), socket, services, names)
}
