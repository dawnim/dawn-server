package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/authsession"
	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"gorm.io/gorm"
)

type Actor struct {
	User         models.AdminUser
	Permissions  map[string]bool
	SessionID    string
	cacheVersion string
}

const DefaultAdminPassword = "123456"

func (a *Actor) Can(key string) bool { return a.User.Super || a.Permissions[key] }

// Departments 返回本部门、指定部门及其全部后代；全部范围不附加部门条件。
func (a *Actor) Departments(db *gorm.DB) ([]uint, bool, error) {
	if a.User.Super {
		return nil, true, nil
	}
	ids := map[uint]bool{}
	for _, role := range a.User.Roles {
		if role.Status != models.Enabled {
			continue
		}
		switch role.DataScope {
		case models.ScopeAll:
			return nil, true, nil
		case models.ScopeDepartment:
			ids[a.User.DepartmentID] = true
		case models.ScopeDepartments:
			for _, id := range role.DepartmentIDs {
				ids[id] = true
			}
		}
	}
	delete(ids, 0)
	if len(ids) > 0 {
		var rows []models.Department
		if err := db.Session(&gorm.Session{NewDB: true}).Select("id", "parent_id").Find(&rows).Error; err != nil {
			return nil, false, err
		}
		for changed := true; changed; {
			changed = false
			for _, row := range rows {
				if ids[row.ParentID] && !ids[row.ID] {
					ids[row.ID], changed = true, true
				}
			}
		}
	}
	departments := make([]uint, 0, len(ids))
	for id := range ids {
		departments = append(departments, id)
	}
	return departments, false, nil
}

// Scope 在业务数据的列表、详情和写入上使用相同范围。
func (a *Actor) Scope(ownerColumn, departmentColumn string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		departments, all, err := a.Departments(db)
		if err != nil {
			db.AddError(err)
			return db
		}
		if all {
			return db
		}
		condition := db.Session(&gorm.Session{NewDB: true})
		if ownerColumn != "" {
			condition = condition.Where(ownerColumn+" = ?", a.User.ID)
		} else {
			condition = condition.Where("1 = 0")
		}
		if departmentColumn != "" && len(departments) > 0 {
			condition = condition.Or(departmentColumn+" IN ?", departments)
		}
		return db.Where(condition)
	}
}

// CanManageUser 校验管理级别；调用方还需检查操作权限和数据范围。
func (a *Actor) CanManageUser(user *models.AdminUser) bool {
	return !user.Super && user.ID != a.User.ID && (a.User.Super || (&Actor{User: *user}).Level() < a.Level())
}
func TokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Authenticate 独立验证登录会话，资料和权限使用按用户隔离的缓存。
func (a *App) Authenticate(token string) (*Actor, error) {
	record, err := a.Sessions.Authenticate(context.Background(), token, false)
	if errors.Is(err, authsession.ErrUnauthenticated) {
		return nil, gorm.ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	return a.ActorForSession(record.ID)
}

// ActorForSession reloads a durable session and its current roles for background work.
func (a *App) ActorForSession(id string) (*Actor, error) {
	ctx := context.Background()
	for {
		record, err := a.Sessions.Read(ctx, id)
		if errors.Is(err, authsession.ErrUnauthenticated) || (err == nil && record.ExpiresAt <= time.Now().UnixMilli()) {
			return nil, gorm.ErrRecordNotFound
		}
		if err != nil {
			return nil, err
		}
		session := record
		version, err := a.userCacheVersion(ctx, session.UserID)
		if err != nil {
			return nil, err
		}
		user, err := cachedUserData(ctx, a, constants.TempUserInfo, session.UserID, version, func() (models.AdminUser, error) {
			var user models.AdminUser
			err := a.DB.Preload("Roles", "status = ?", models.Enabled).Preload("Posts").First(&user, session.UserID).Error
			return user, err
		})
		if err != nil {
			return nil, err
		}
		if user.Status != models.Enabled {
			return nil, gorm.ErrRecordNotFound
		}
		permissions, err := cachedUserData(ctx, a, constants.TempUserPermissions, user.ID, version, func() (map[string]bool, error) {
			ids := make([]uint, 0, len(user.Roles))
			for _, role := range user.Roles {
				ids = append(ids, role.ID)
			}
			var grants []models.RolePermission
			if len(ids) > 0 {
				if err := a.DB.Where("role_id IN ?", ids).Find(&grants).Error; err != nil {
					return nil, err
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
			permissions := map[string]bool{}
			for _, grant := range grants {
				if !strings.HasPrefix(grant.Key, "custom.") || active[grant.Key] {
					permissions[grant.Key] = true
				}
			}
			return permissions, nil
		})
		if err != nil {
			return nil, err
		}
		currentVersion, err := a.userCacheVersion(ctx, session.UserID)
		if err != nil {
			return nil, err
		}
		// 授权读取期间发生变更时重新读取，避免混用不同版本的资料和权限。
		if currentVersion != version {
			continue
		}
		return &Actor{User: user, SessionID: session.ID, Permissions: permissions, cacheVersion: version}, nil
	}
}

func (a *Actor) Level() int {
	if a.User.Super {
		return math.MaxInt
	}
	level := 0
	for _, role := range a.User.Roles {
		if role.Status == models.Enabled && role.Level > level {
			level = role.Level
		}
	}
	return level
}

// CheckRoles 仅允许分配启用的下级角色，不要求操作者拥有其业务权限。
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
		if role.Status != models.Enabled {
			return i18n.Errorf("不能分配停用角色")
		}
		if !current.User.Super && role.Level >= current.Level() {
			return i18n.Errorf("不能分配同级或更高级角色")
		}
	}
	return nil
}
