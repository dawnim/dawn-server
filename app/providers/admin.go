package providers

import (
	"cmp"
	"context"
	"fmt"
	"github.com/dawnim/dawn-server/support/geoip"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/cmd"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/container"
	cc "github.com/prismgo/framework/contracts/container"
	pc "github.com/prismgo/framework/contracts/provider"
	rc "github.com/prismgo/framework/contracts/redis"
	"github.com/prismgo/framework/filesystem"
	"github.com/prismgo/framework/kernel"
	"github.com/prismgo/framework/provider"
	"github.com/prismgo/framework/support"
	"gorm.io/gorm"
)

// AdminServiceProvider 将后台能力绑定到当前应用。
type AdminServiceProvider struct{ Configure func(*admin.Options) }

// Register declares lazy services; database/network work happens on resolution.
func (p AdminServiceProvider) Register(application pc.Application) error {
	return application.Container().Singleton("dawn.admin", func(r cc.Resolver) (any, error) {
		cfgRaw, err := r.Make("config.default")
		if err != nil {
			return nil, err
		}
		cfg := cfgRaw.(*config.Config)
		o := admin.Options{
			Name: cfg.GetString("app.name", "dawn"), Environment: cfg.GetString("app.env", "local"),
			WSAddr: cfg.GetString("dawn.ws_addr", ":8851"), WSPath: cfg.GetString("dawn.ws_path", "/"),
			AdminPrefix: cfg.GetString("dawn.admin_prefix", "/admin"), Storage: cfg.GetString("dawn.storage", support.StoragePath()),
			AllowedOrigins: strings.Fields(cfg.GetString("dawn.allowed_origins", "*")),
		}
		if p.Configure != nil {
			p.Configure(&o)
		}
		if o.AccessTTL == 0 {
			o.AccessTTL, err = time.ParseDuration(cfg.GetString("dawn.access_ttl", "720h"))
			if err != nil {
				return nil, err
			}
		}
		if o.RefreshTTL == 0 {
			o.RefreshTTL, err = time.ParseDuration(cfg.GetString("dawn.refresh_ttl", "720h"))
			if err != nil {
				return nil, err
			}
		}
		raw, err := r.Make("database.default")
		if err != nil {
			return nil, err
		}
		connection, err := r.Make("redis.connection")
		if err != nil {
			return nil, err
		}
		manager, err := filesystem.ManagerFrom(r)
		if err != nil {
			return nil, err
		}
		disk := o.Disk
		if disk == nil {
			disk = manager.Disk(cmp.Or(cfg.GetString("dawn.disk"), cfg.GetString("filesystem.default", "local")))
		}
		runtime, ok := application.(admin.Runtime)
		if !ok {
			return nil, fmt.Errorf("dawn: application does not provide HTTP runtime")
		}
		db := raw.(*gorm.DB)
		db.Config.TranslateError = true
		return admin.New(o, runtime, db, connection.(rc.Connection).Client(), disk)
	}, container.WithCloser(func(a *admin.App) error { a.Realtime.Close(); return nil }))
}

// Boot 注册命令工厂，执行时才解析后台资源。
func (AdminServiceProvider) Boot(app pc.Application) error {
	registrar, err := app.Container().Make(kernel.StartingRegistrarKey)
	if err != nil {
		return err
	}
	if err := registrar.(kernel.StartingRegistrar)(func(k *kernel.Kernel) error {
		k.Schedule().Call(func(ctx context.Context) error { return geoip.Update(ctx) }).DailyAt("03:30").Name("admin.geoip.update").WithoutOverlapping(30)
		return nil
	}); err != nil {
		return err
	}

	if err := i18n.Register(); err != nil {
		return err
	}
	return provider.Commands(
		console.CommandFactory(func() console.Command { return &cmd.StartCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.ServiceCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.MigrateCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.InstallCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.GeoIPCommand{} }),
	)
}
