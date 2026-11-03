// Package constants 集中描述 Key 模板, 系统和业务使用相同定义类型.
package constants

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Definition struct {
	Name        string        `json:"name"`
	Pattern     string        `json:"pattern"`
	Type        string        `json:"type"`
	Description string        `json:"description"`
	TTL         time.Duration `json:"ttl"`
	Readable    bool          `json:"readable"`
	Editable    bool          `json:"editable"`
	Clearable   bool          `json:"clearable"` // 是否纳入顶栏批量清理，不限制手动删除
}

// Format 统一参数替换, 业务不再散落拼接 Key.
func (d Definition) Format(args ...any) string { return fmt.Sprintf(d.Pattern, args...) }

// MatchPattern 生成后台 SCAN 模式, 保留定义中的固定部分.
func (d Definition) MatchPattern() string {
	return strings.NewReplacer("%s", "*", "%d", "*").Replace(d.Pattern)
}

// Matches 约束实例必须属于模板；参数不能跨越命名空间。
func (d Definition) Matches(prefix, key string) bool {
	pattern := strings.NewReplacer("%s", "[^:]+", "%d", "[0-9]+").Replace(regexp.QuoteMeta(prefix + d.Pattern))
	return regexp.MustCompile("^" + pattern + "$").MatchString(key)
}

var (
	TempChatDispatch        = Definition{Name: "TempChatDispatch", Pattern: "temp:admin:chat_dispatch", Type: "list", Description: "聊天投递唤醒信号", TTL: 24 * time.Hour}
	ForeverUserCacheVersion = Definition{Name: "ForeverUserCacheVersion", Pattern: "forever:admin:user_cache_version:%d", Type: "string", Description: "管理员授权缓存版本"}
	ForeverSettings         = Definition{Name: "ForeverSettings", Pattern: "forever:admin:settings:%s", Type: "hash", Description: "全局配置缓存", Readable: true, Clearable: true}
	TempCaptcha             = Definition{Name: "TempCaptcha", Pattern: "temp:admin:captcha:%s", Type: "string", Description: "一次性登录验证码", TTL: 5 * time.Minute}
	TempLoginLimit          = Definition{Name: "TempLoginLimit", Pattern: "temp:admin:login_limit:%s", Type: "string", Description: "登录失败次数", TTL: 15 * time.Minute}
	TempUserInfo            = Definition{Name: "TempUserInfo", Pattern: "temp:admin:user_info:%d", Type: "string", Description: "管理员资料缓存", TTL: 4 * time.Hour, Readable: true, Clearable: true}
	TempUserPermissions     = Definition{Name: "TempUserPermissions", Pattern: "temp:admin:user_permissions:%d", Type: "string", Description: "管理员权限缓存", TTL: 2 * time.Hour, Readable: true, Clearable: true}
	TempUserMenus           = Definition{Name: "TempUserMenus", Pattern: "temp:admin:user_menus:%d", Type: "string", Description: "管理员菜单缓存", TTL: 7 * 24 * time.Hour, Readable: true, Clearable: true}
	TempWSTicket            = Definition{Name: "TempWSTicket", Pattern: "temp:admin:ws_ticket:%s", Type: "string", Description: "WS一次性连接凭证", TTL: time.Minute}
	TempWSSession           = Definition{Name: "TempWSSession", Pattern: "temp:admin:ws_session:%s", Type: "string", Description: "WS逻辑会话", TTL: 24 * time.Hour}
	TempWSLease             = Definition{Name: "TempWSLease", Pattern: "temp:admin:ws_lease:%s", Type: "string", Description: "WS物理连接租约", TTL: time.Minute}
	TempWSStream            = Definition{Name: "TempWSStream", Pattern: "temp:admin:ws_stream:%s", Type: "stream", Description: "可恢复实时消息", TTL: 24 * time.Hour}
	TempWSCursor            = Definition{Name: "TempWSCursor", Pattern: "temp:admin:ws_cursor:%s:%s", Type: "string", Description: "设备消息确认进度", TTL: 24 * time.Hour}
	TempUpload              = Definition{Name: "TempUpload", Pattern: "temp:admin:upload:%s", Type: "string", Description: "分片上传清单", TTL: 24 * time.Hour}
	TempLinkTicket          = Definition{Name: "TempLinkTicket", Pattern: "temp:admin:link_ticket:%s", Type: "string", Description: "外部系统一次性登录票据", TTL: time.Minute}
	TempLinkSession         = Definition{Name: "TempLinkSession", Pattern: "temp:admin:link_session:%s", Type: "string", Description: "外部系统短时鉴权会话", TTL: 15 * time.Minute}
)

func System() []Definition {
	return []Definition{TempChatDispatch, ForeverUserCacheVersion, ForeverSettings, TempCaptcha, TempLoginLimit, TempUserInfo, TempUserPermissions, TempUserMenus, TempWSTicket, TempWSSession, TempWSLease, TempWSStream, TempWSCursor, TempUpload, TempLinkTicket, TempLinkSession}
}
