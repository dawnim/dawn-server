package admin

import (
	"context"
	"time"

	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/gin-gonic/gin"
	fs "github.com/prismgo/framework/contracts/filesystem"
)

// Options 仅声明应用实际需要配置的入口, 业务直接追加代码定义.
type Options struct {
	Name           string
	Environment    string
	WSAddr         string
	WSPath         string
	AdminPrefix    string
	Storage        string
	Disk           fs.Filesystem
	AllowedOrigins []string
	Menus          []Menu
	Permissions    []Permission
	CacheKeys      []constants.Definition
	Settings       []SettingDefinition
	LinkSystems    []LinkSystem
	Routes         func(*App, *gin.RouterGroup)
	PublicRoutes   func(*App, *gin.Engine)
	WSValidate     func(context.Context, realtime.Principal) error
	WSAuthorize    func(context.Context, realtime.Principal, string) error
	Realtime       func(*App, *realtime.Hub) error
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
}
