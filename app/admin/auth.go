package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Actor struct {
	User        models.User
	Permissions map[string]bool
	SessionID   string
}

const DefaultAdminPassword = "123456"

func (a *Actor) Can(key string) bool { return a.User.Super || a.Permissions[key] }

// Scope 在列表、详情和写入请求上使用同样的数据范围.
func (a *Actor) Scope(ownerColumn, departmentColumn string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if a.User.Super {
			return db
		}
		departments := []uint{}
		for _, role := range a.User.Roles {
			if role.Status != models.Enabled {
				continue
			}
			switch role.DataScope {
			case models.ScopeAll:
				return db
			case models.ScopeDepartment:
				departments = append(departments, a.User.DepartmentID)
			case models.ScopeDepartments:
				departments = append(departments, role.DepartmentIDs...)
			}
		}
		departments = slices.DeleteFunc(departments, func(id uint) bool { return id == 0 })
		condition := db.Session(&gorm.Session{NewDB: true}).Where(ownerColumn+" = ?", a.User.ID)
		if departmentColumn != "" && len(departments) > 0 {
			condition = condition.Or(departmentColumn+" IN ?", departments)
		}
		return db.Where(condition)
	}
}
func TokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Authenticate 从数据库读取当前账号和角色, 修改授权无需等待旧缓存过期.
func (a *App) Authenticate(token string) (*Actor, error) {
	return a.authenticateSession("access_hash", TokenHash(token))
}
func (a *App) authenticateSession(column, value string) (*Actor, error) {
	var session models.Session
	if err := a.DB.Where(map[string]any{column: value}).Where("expires_at > ?", time.Now().UnixMilli()).First(&session).Error; err != nil {
		return nil, err
	}
	var user models.User
	if err := a.DB.Preload("Roles", "status = ?", models.Enabled).Preload("Posts").Where("status = ?", models.Enabled).First(&user, session.UserID).Error; err != nil {
		return nil, err
	}
	result := &Actor{User: user, SessionID: session.ID, Permissions: map[string]bool{}}
	ids := []uint{}
	for _, role := range user.Roles {
		ids = append(ids, role.ID)
	}
	if len(ids) > 0 {
		var permissions []models.RolePermission
		if err := a.DB.Where("role_id IN ?", ids).Find(&permissions).Error; err != nil {
			return nil, err
		}
		for _, permission := range permissions {
			result.Permissions[permission.Key] = true
		}
	}
	var custom []models.Menu
	if err := a.DB.Find(&custom).Error; err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, menu := range custom {
		active[menu.Key] = menu.Status == models.Enabled
	}
	for key := range result.Permissions {
		if strings.HasPrefix(key, "custom.") && !active[key] {
			delete(result.Permissions, key)
		}
	}
	return result, nil
}

// CreateAdmin 只在显式命令中创建账号, 不重置已有管理员密码.
func (a *App) CreateAdmin(username, password string) error {
	if len(password) < 6 || len(password) > 72 {
		return i18n.Errorf("密码长度须为6至72字符")
	}
	var existing models.User
	if err := a.DB.Where("username = ?", username).Limit(1).Find(&existing).Error; err != nil {
		return err
	}
	if existing.ID != 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.DB.Create(&models.User{Username: username, Password: string(hash), Realname: i18n.Text("", "超级管理员"), Status: models.Enabled, Super: true}).Error
}
func (a *Actor) Level() int {
	if a.User.Super {
		return 0
	}
	level := math.MaxInt
	for _, role := range a.User.Roles {
		if role.Status == models.Enabled && role.Level < level {
			level = role.Level
		}
	}
	return level
}

// CheckRoles 防止管理员给自己或下属分配超出自身权限的角色.
func (a *App) CheckRoles(current *Actor, ids []uint) error {
	var roles []models.Role
	if len(ids) == 0 {
		return nil
	}
	if err := a.DB.Where("id IN ?", ids).Find(&roles).Error; err != nil {
		return err
	}
	if len(roles) != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return i18n.Errorf("包含不存在的角色")
	}
	for _, role := range roles {
		if current.User.Super {
			continue
		}
		if role.Level <= current.Level() {
			return i18n.Errorf("不能分配同级或更高级角色")
		}
		if role.DataScope != models.ScopeSelf {
			return i18n.Errorf("只有超级管理员可以分配扩展数据范围的角色")
		}
		var grants []models.RolePermission
		if err := a.DB.Where("role_id = ?", role.ID).Find(&grants).Error; err != nil {
			return err
		}
		for _, grant := range grants {
			if !current.Can(grant.Key) {
				return i18n.Errorf("角色包含超出当前管理员范围的权限")
			}
		}
	}
	return nil
}

// ActorForSession reloads a durable session and its current roles for background work.
func (a *App) ActorForSession(id string) (*Actor, error) { return a.authenticateSession("id", id) }
