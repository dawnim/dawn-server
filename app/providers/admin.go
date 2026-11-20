package providers

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/clientip"
	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/dawnim/dawn-server/support/storage"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/cmd"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/database/schema"
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
type AdminServiceProvider struct {
	Configure func(*admin.Options)
	Schema    func(*schema.Definition)
}

// Register declares lazy services; database/network work happens on resolution.
func (p AdminServiceProvider) Register(application pc.Application) error {
	if err := application.Container().Singleton("dawn.schema", func(cc.Resolver) (any, error) {
		definition := migrations.Definition()
		if p.Schema != nil {
			p.Schema(definition)
		}
		return definition, nil
	}); err != nil {
		return err
	}
	if err := application.Container().Singleton("dawn.clientip", func(r cc.Resolver) (any, error) {
		raw, err := r.Make("config.default")
		if err != nil {
			return nil, err
		}
		cfg := raw.(*config.Config)
		mode := cfg.GetString("app.server.client_ip_mode", "trusted")
		headers := cfg.GetString("app.server.client_ip_headers", "X-Forwarded-For,X-Real-IP")
		if mode == "simple" {
			headers = cfg.GetString("app.server.client_ip_simple_headers", strings.Join(clientip.DefaultHeaders, ","))
		}
		return clientip.New(clientip.Config{
			Mode:           mode,
			Headers:        strings.Split(headers, ","),
			TrustedProxies: strings.Split(cfg.GetString("app.server.trusted_proxies", ""), ","),
			Cloudflare:     cfg.GetBool("app.server.cloudflare_proxies", false),
			CloudFront:     cfg.GetBool("app.server.cloudfront_proxies", false),
			Directory:      support.StoragePath("app", "clientip"),
		})
	}, container.WithCloser(func(r *clientip.Resolver) error { return r.Close() })); err != nil {
		return err
	}
	if err := application.Container().Singleton("dawn.geoip", func(r cc.Resolver) (any, error) {
		raw, err := r.Make("config.default")
		if err != nil {
			return nil, err
		}
		cfg := raw.(*config.Config)
		sources := cfg.GetStringMapString("dawn.geoip.sources")
		return geoip.New(geoip.Config{Enabled: cfg.GetBool("dawn.geoip.enabled", true), Directory: cfg.GetString("dawn.geoip.directory", support.StoragePath("app", "geoip")), Sources: sources}), nil
	}, container.WithCloser(func(m *geoip.Manager) error { return m.Close() })); err != nil {
		return err
	}
	if err := application.Container().Singleton("dawn.customer", func(r cc.Resolver) (any, error) {
		a, err := admin.From(r)
		if err != nil {
			return nil, err
		}
		return a.Customer, nil
	}); err != nil {
		return err
	}
	if err := application.Container().Singleton("dawn.settings", func(r cc.Resolver) (any, error) {
		a, err := admin.From(r)
		if err != nil {
			return nil, err
		}
		return a.Settings, nil
	}); err != nil {
		return err
	}
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
		storage.Register(manager)
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
		console.CommandFactory(func() console.Command { return &cmd.EmbeddingCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.ServiceCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.InstallCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.SchemaCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.GenerateCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.RenameCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.GeoIPCommand{} }),
		console.CommandFactory(func() console.Command { return &cmd.ModelCheckCommand{} }),
	)
}
