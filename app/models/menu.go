package models

// Menu 保存自定义目录、外链菜单和操作权限, 内置菜单由代码定义.
type Menu struct {
	Base
	Type      int    `json:"type" gorm:"not null;default:0;comment:类型:1目录 2菜单 3按钮 4外链" binding:"required,oneof=1 2 3 4"`
	Path      string `json:"path" gorm:"not null;size:500;comment:前端路由路径"`
	Component string `json:"component" gorm:"not null;size:500;comment:前端组件路径"`
	Status    int    `json:"status" gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用" binding:"required,oneof=1 2"`
	Hidden    bool   `json:"hidden" gorm:"not null;default:false;comment:隐藏菜单:0否 1是"`
	KeepAlive bool   `json:"keep_alive" gorm:"not null;default:false;comment:缓存页面:0否 1是"`
	FixedTab  bool   `json:"fixed_tab" gorm:"not null;default:false;comment:固定页签:0否 1是"`
	FullPage  bool   `json:"full_page" gorm:"not null;default:false;comment:独立全屏:0否 1是"`
	Key       string `json:"key" gorm:"not null;size:150;uniqueIndex;comment:菜单或权限唯一Key:保存后不可修改" binding:"required,max=150"`
	ParentKey string `json:"parent_key" gorm:"not null;size:150;index;comment:上级菜单Key:空为顶级"`
	Title     string `json:"title" gorm:"not null;size:100;comment:菜单或权限名称" binding:"required,max=100"`
	URL       string `json:"url" gorm:"not null;size:1500;comment:菜单目标地址:站内路径或HTTP(S)地址" binding:"max=1500"`
	Icon      string `json:"icon" gorm:"not null;size:100;comment:菜单图标"`
	Mode      string `json:"mode" gorm:"not null;size:20;comment:打开方式:iframe后台内嵌 new_tab新窗口"`
	SystemKey string `json:"system_key" gorm:"not null;size:100;comment:受控外部系统Key"`
	Sort      int    `json:"sort" gorm:"not null;default:0;comment:排序:升序"`
}

func (Menu) TableName() string { return "admin_menus" }

const (
	MenuDirectory = 1
	MenuPage      = 2
	MenuAction    = 3
	MenuLink      = 4
)
