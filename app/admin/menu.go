package admin

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/models"
)

type Permission struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	ParentKey string `json:"parent_key"`
}
type Menu struct {
	Type       int    `json:"type"`
	Key        string `json:"key"`
	ParentKey  string `json:"parent_key"`
	Title      string `json:"title"`
	Path       string `json:"path"`
	Component  string `json:"component,omitempty"`
	Permission string `json:"permission,omitempty"`
	Icon       string `json:"icon,omitempty"`
	Sort       int    `json:"sort"`
	URL        string `json:"url,omitempty"`
	Mode       string `json:"mode,omitempty"`
	SystemKey  string `json:"system_key,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Status     int    `json:"status"`
	KeepAlive  bool   `json:"keep_alive"`
	FixedTab   bool   `json:"fixed_tab"`
	FullPage   bool   `json:"full_page"`
	Children   []Menu `json:"children,omitempty"`
}

var StableKey = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func (a *App) registerMenus() error {
	a.MenuDefinitions = []Menu{
		{Key: "dawn.dashboard", Title: "仪表盘", Path: "/dashboard", Icon: "ri:pie-chart-line", Sort: 0},
		{Key: "dawn.home", ParentKey: "dawn.dashboard", Title: "工作台", Path: "/dashboard/console", Component: "/dashboard/console", Icon: "ri:home-smile-2-line", Sort: 0},
		{Key: "dawn.profile", ParentKey: "dawn.dashboard", Title: "个人中心", Path: "/dashboard/user-center", Component: "/dashboard/user-center", Hidden: true, Sort: 1000},
		{Key: "dawn.system", Title: "系统管理", Path: "/system", Icon: "ri:user-3-line", Sort: 10},
		{Key: "dawn.maintenance", Title: "运维管理", Path: "/maintenance", Icon: "ri:shield-check-line", Sort: 20},
		{Key: "dawn.development", Title: "开发工具", Path: "/development", Icon: "ri:tools-line", Sort: 30},
	}
	icons := map[string]string{"users": "ri:user-line", "roles": "ri:admin-line", "departments": "ri:node-tree", "posts": "ri:signpost-line", "menus": "ri:menu-line", "settings": "ri:settings-4-line", "attachments": "ri:file-cloud-line", "logs": "ri:shield-keyhole-line", "cache": "ri:keyboard-box-line", "server": "ri:server-line", "database": "ri:database-line", "generator": "ri:code-s-slash-line", "horizon": "ri:stack-line"}
	titles := map[string]string{"list": "查看", "create": "新增", "update": "修改", "delete": "删除", "export": "导出"}
	for _, item := range []struct{ name, title, parent, component, actions string }{
		{"users", "用户管理", "dawn.system", "/system/user", "list create update delete"},
		{"departments", "部门管理", "dawn.system", "/system/dept", "list create update delete export"},
		{"roles", "角色管理", "dawn.system", "/system/role", "list create update delete export"},
		{"posts", "岗位管理", "dawn.system", "/system/post", "list create update delete export"},
		{"menus", "菜单管理", "dawn.system", "/system/menu", "list create update delete export"},
		{"settings", "系统设置", "dawn.system", "/system/config", "list update"},
		{"attachments", "附件管理", "dawn.maintenance", "/safeguard/attachment", "list create update delete export"},
		{"logs", "操作日志", "dawn.maintenance", "/safeguard/oper-log", "list delete"},
		{"cache", "缓存管理", "dawn.maintenance", "/safeguard/cache", "list update delete"},
		{"server", "服务监控", "dawn.maintenance", "/safeguard/server", "list"},
		{"database", "数据表维护", "dawn.maintenance", "/safeguard/database", "list update"},
		{"generator", "代码生成", "dawn.development", "/tool/code", "list create"},
		{"horizon", "队列监控", "dawn.maintenance", "/safeguard/horizon", "list"},
	} {
		key := "dawn." + item.name
		a.MenuDefinitions = append(a.MenuDefinitions, Menu{Key: key, ParentKey: item.parent, Title: item.title, Path: item.component, Component: item.component, Permission: key + ".list", Icon: icons[item.name], Sort: map[string]int{"users": 10, "departments": 20, "roles": 30, "posts": 40, "menus": 50, "settings": 60, "attachments": 10, "database": 20, "server": 30, "cache": 40, "logs": 60, "generator": 10, "horizon": 80}[item.name]})
		for _, action := range strings.Fields(item.actions) {
			permission := Permission{Key: key + "." + action, Title: titles[action], ParentKey: key}
			a.PermissionDefinitions[permission.Key] = permission
		}
	}
	for _, p := range a.Options.Permissions {
		if !StableKey.MatchString(p.Key) || strings.HasPrefix(p.Key, "dawn.") || strings.HasPrefix(p.Key, "custom.") {
			return i18n.Errorf("业务权限Key无效或占用保留命名空间: %s", p.Key)
		}
		if _, exists := a.PermissionDefinitions[p.Key]; exists {
			return i18n.Errorf("权限Key重复: %s", p.Key)
		}
		a.PermissionDefinitions[p.Key] = p
	}
	a.MenuDefinitions = append(a.MenuDefinitions,
		Menu{Key: "dawn.login_logs", ParentKey: "dawn.maintenance", Title: "登录日志", Path: "/safeguard/login-log", Component: "/safeguard/login-log", Permission: "dawn.logs.list", Icon: "ri:login-circle-line", Sort: 50},
		Menu{Key: "dawn.email_logs", ParentKey: "dawn.maintenance", Title: "邮件日志", Path: "/safeguard/email-log", Component: "/safeguard/email-log", Permission: "dawn.logs.list", Icon: "ri:mail-line", Sort: 70},
	)
	for _, menu := range a.Options.Menus {
		if !strings.HasPrefix(menu.Key, "app.") {
			return i18n.Errorf("业务菜单必须使用app.命名空间: %s", menu.Key)
		}
	}
	a.MenuDefinitions = append(a.MenuDefinitions, a.Options.Menus...)
	keys := map[string]Menu{}
	for i := range a.MenuDefinitions {
		m := &a.MenuDefinitions[i]
		m.Type = models.MenuDirectory
		if m.Component != "" || m.URL != "" {
			m.Type = models.MenuPage
		}
		if m.URL != "" {
			m.Type = models.MenuLink
		}
		m.Status = models.Enabled
		if !StableKey.MatchString(m.Key) {
			return i18n.Errorf("菜单Key无效: %s", m.Key)
		}
		if _, exists := keys[m.Key]; exists {
			return i18n.Errorf("菜单Key重复: %s", m.Key)
		}
		if _, exists := a.PermissionDefinitions[m.Key]; exists {
			return i18n.Errorf("菜单与权限Key冲突: %s", m.Key)
		}
		keys[m.Key] = *m
	}
	for _, m := range a.MenuDefinitions {
		if m.ParentKey != "" {
			if p, ok := keys[m.ParentKey]; !ok || p.Type != models.MenuDirectory {
				return i18n.Errorf("菜单父目录无效: %s", m.Key)
			}
		}
		if m.Permission != "" {
			if _, ok := a.PermissionDefinitions[m.Permission]; !ok {
				return i18n.Errorf("菜单引用未注册权限: %s", m.Permission)
			}
		}
		seen := map[string]bool{m.Key: true}
		for p := m.ParentKey; p != ""; p = keys[p].ParentKey {
			if seen[p] {
				return i18n.Errorf("菜单目录循环: %s", m.Key)
			}
			seen[p] = true
		}
	}
	return nil
}

// Menus 合并代码菜单和自定义菜单, 仅返回当前用户有权访问的页面.
func (a *App) Menus(actor *Actor, locale string) ([]Menu, error) {
	var menus []models.Menu
	if err := a.DB.Where("type <> ? AND status = ?", models.MenuAction, models.Enabled).Order("sort, id").Find(&menus).Error; err != nil {
		return nil, err
	}
	all := a.MenuList(locale)
	for _, item := range menus {
		if _, collision := a.PermissionDefinitions[item.Key]; collision {
			return nil, i18n.Errorf("自定义菜单与权限Key冲突: %s", item.Key)
		}
		for _, m := range a.MenuDefinitions {
			if m.Key == item.Key {
				return nil, i18n.Errorf("自定义菜单与代码菜单Key冲突: %s", item.Key)
			}
		}
		menu := Menu{Type: item.Type, Key: item.Key, ParentKey: item.ParentKey, Title: item.Title, Path: "/links/" + item.Key, Icon: item.Icon, Sort: item.Sort, Status: item.Status, Hidden: item.Hidden, KeepAlive: item.KeepAlive, FixedTab: item.FixedTab, FullPage: item.FullPage}
		if item.Type == models.MenuPage {
			menu.Path, menu.Component, menu.Permission = item.Path, item.Component, item.Key
		}
		if item.Type == models.MenuLink {
			menu.Component, menu.Permission = "/outside/Iframe", item.Key
			menu.URL, menu.Mode, menu.SystemKey = item.URL, item.Mode, item.SystemKey
		}
		all = append(all, menu)
	}
	slices.SortStableFunc(all, func(x, y Menu) int { return x.Sort - y.Sort })
	var tree func(string) []Menu
	tree = func(parent string) []Menu {
		result := []Menu{}
		for _, m := range all {
			if m.ParentKey != parent {
				continue
			}
			m.Children = tree(m.Key)
			if m.Type == models.MenuDirectory {
				if len(m.Children) == 0 {
					continue
				}
			} else if m.Permission != "" && !actor.Can(m.Permission) {
				continue
			}
			result = append(result, m)
		}
		return result
	}
	return tree(""), nil
}
func (a *App) ValidateMenu(menu *models.Menu, editing bool) error {
	if !StableKey.MatchString(menu.Key) || !strings.HasPrefix(menu.Key, "custom.") {
		return i18n.Errorf("自定义菜单Key必须使用custom.命名空间")
	}
	if editing {
		var old models.Menu
		if err := a.DB.First(&old, menu.ID).Error; err != nil {
			return err
		}
		if old.Key != menu.Key {
			return i18n.Errorf("已经保存的Key不允许改名")
		}
	}
	var count int64
	if err := a.DB.Unscoped().Model(&models.Menu{}).Where(map[string]any{"key": menu.Key}).Where("id <> ?", menu.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return i18n.Errorf("Key已存在或已经被保留")
	}
	parents := map[string]Menu{}
	for _, m := range a.MenuDefinitions {
		if m.Key == menu.Key {
			return i18n.Errorf("Key与代码菜单冲突")
		}
		parents[m.Key] = m
	}
	if _, exists := a.PermissionDefinitions[menu.Key]; exists {
		return i18n.Errorf("Key与权限定义冲突")
	}
	var saved []models.Menu
	if err := a.DB.Find(&saved).Error; err != nil {
		return err
	}
	parentTypes := map[int]int{models.MenuDirectory: models.MenuDirectory, models.MenuPage: models.MenuDirectory, models.MenuAction: models.MenuPage, models.MenuLink: models.MenuDirectory}
	for _, item := range saved {
		parents[item.Key] = Menu{Key: item.Key, ParentKey: item.ParentKey, Type: item.Type}
		if item.ParentKey == menu.Key && parentTypes[item.Type] != menu.Type && !(item.Type == models.MenuAction && menu.Type == models.MenuLink) {
			return i18n.Errorf("请先调整子项，再修改菜单类型")
		}
	}
	if (menu.ParentKey == "" && menu.Type == models.MenuAction) || (menu.ParentKey != "" && parents[menu.ParentKey].Type != parentTypes[menu.Type] && !(menu.Type == models.MenuAction && parents[menu.ParentKey].Type == models.MenuLink)) {
		return i18n.Errorf("请选择目录，按钮必须挂在菜单下")
	}
	seen := map[string]bool{menu.Key: true}
	for key := menu.ParentKey; key != ""; key = parents[key].ParentKey {
		if seen[key] {
			return i18n.Errorf("菜单目录循环: %s", menu.Key)
		}
		seen[key] = true
	}
	if menu.Type == models.MenuPage && (!strings.HasPrefix(menu.Path, "/") || !strings.HasPrefix(menu.Component, "/")) {
		return i18n.Errorf("请填写以/开头的路由和组件路径")
	}
	if menu.Type != models.MenuPage {
		menu.Path, menu.Component = "", ""
	}
	if menu.Type != models.MenuLink {
		menu.URL, menu.Mode, menu.SystemKey = "", "", ""
		return nil
	}
	u, err := url.Parse(menu.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && !(u.Scheme == "" && strings.HasPrefix(menu.URL, "/") && !strings.HasPrefix(menu.URL, "//"))) {
		return i18n.Errorf("只支持站内路径和HTTP(S)地址")
	}
	if menu.Mode != "new_tab" && menu.Mode != "iframe" {
		return i18n.Errorf("打开方式应为new_tab或iframe")
	}
	if menu.Mode == "iframe" && u.Host != "" && !slices.Contains(a.Options.AllowedOrigins, "*") && !slices.Contains(a.Options.AllowedOrigins, u.Scheme+"://"+u.Host) {
		return i18n.Errorf("内嵌页面来源未加入ALLOWED_ORIGINS")
	}
	if u.User != nil || (u.Scheme != "" && u.Host == "") {
		return i18n.Errorf("外链地址格式错误")
	}
	if menu.SystemKey != "" {
		found := false
		for _, system := range a.Options.LinkSystems {
			if system.Key == menu.SystemKey && system.Origin == u.Scheme+"://"+u.Host {
				found = true
			}
		}
		if !found {
			return i18n.Errorf("外链系统未注册或来源不匹配")
		}
	}
	return nil
}

func (a *App) MenuList(locale string) []Menu {
	menus := slices.Clone(a.MenuDefinitions)
	for i := range menus {
		menus[i].Title = i18n.Text(locale, menus[i].Title)
	}
	return menus
}

func (a *App) PermissionList(locale string) map[string]Permission {
	permissions := make(map[string]Permission, len(a.PermissionDefinitions))
	for key, p := range a.PermissionDefinitions {
		p.Title = i18n.Text(locale, p.Title)
		permissions[key] = p
	}
	return permissions
}
