package admin

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	fs "github.com/prismgo/framework/contracts/filesystem"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Runtime 提供应用已有的HTTP服务与退出能力。
type Runtime interface {
	NewHTTPServer(context.Context, string) (*http.Server, error)
	Close(...time.Duration) error
}

// App 持有后台定义和容器提供的共享资源。
type App struct {
	Options               Options
	DB                    *gorm.DB
	Redis                 redis.UniversalClient
	Runtime               Runtime
	Router                *gin.Engine
	Disk                  fs.Filesystem
	Realtime              *realtime.Hub
	CacheKeys             map[string]constants.Definition
	PermissionDefinitions map[string]Permission
	MenuDefinitions       []Menu
	SettingDefinitions    map[string]SettingDefinition
}

func New(o Options, runtime Runtime, db *gorm.DB, client redis.UniversalClient, disk fs.Filesystem) (*App, error) {
	o.Name = cmp.Or(o.Name, "dawn")
	o.Environment = cmp.Or(o.Environment, "local")
	o.WSAddr = cmp.Or(o.WSAddr, ":8851")
	o.WSPath = cmp.Or(o.WSPath, "/")
	o.AdminPrefix = "/" + strings.Trim(cmp.Or(o.AdminPrefix, "/admin"), "/")
	o.Storage = cmp.Or(o.Storage, "storage")
	o.AccessTTL = cmp.Or(o.AccessTTL, 30*24*time.Hour)
	o.RefreshTTL = cmp.Or(o.RefreshTTL, 30*24*time.Hour)

	if err := os.MkdirAll(o.Storage, 0750); err != nil {
		return nil, err
	}
	a := &App{Options: o, Runtime: runtime, DB: db, Redis: client, Disk: disk, CacheKeys: map[string]constants.Definition{}, PermissionDefinitions: map[string]Permission{}, SettingDefinitions: map[string]SettingDefinition{}}
	for _, d := range append(constants.System(), o.CacheKeys...) {
		if _, exists := a.CacheKeys[d.Name]; exists {
			return nil, i18n.Errorf("缓存Key名称重复: %s", d.Name)
		}
		for _, old := range a.CacheKeys {
			if old.Pattern == d.Pattern {
				return nil, i18n.Errorf("缓存Key模板重复: %s", d.Pattern)
			}
		}
		a.CacheKeys[d.Name] = d
	}
	for _, d := range append(systemSettings(), o.Settings...) {
		if _, exists := a.SettingDefinitions[d.Key]; exists {
			return nil, i18n.Errorf("配置Key重复: %s", d.Key)
		}
		a.SettingDefinitions[d.Key] = d
	}
	if err := a.registerMenus(); err != nil {
		return nil, err
	}

	a.initRealtime()
	if o.Realtime != nil {
		if err := o.Realtime(a, a.Realtime); err != nil {
			a.Realtime.Close()
			return nil, err
		}
	}
	return a, nil
}

// Key adds the application/environment namespace to a registered key pattern.
func (a *App) Key(d constants.Definition, args ...any) string {
	return a.Options.Name + ":" + a.Options.Environment + ":" + d.Format(args...)
}

// Handler 返回应用装配后的HTTP处理链。
func (a *App) Handler() (http.Handler, error) {
	if a.Router != nil {
		return a.Router, nil
	}
	server, err := a.Runtime.NewHTTPServer(context.Background(), config.GetString("app.server.port", "5123"))
	if err != nil {
		return nil, err
	}
	return server.Handler, nil
}

// Close shuts down the owning application.
func (a *App) Close() error { return a.Runtime.Close() }
