package tests

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/exception"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

func TestAuthorizationCacheInvalidationDuringLoad(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	role := models.Role{Name: "缓存并发", Code: "cache-race", Level: 10, Status: 1, DataScope: models.ScopeAll}
	if err := a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	user := models.User{Username: "cache-race", Status: 1, Roles: []models.Role{role}}
	if err := a.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: "dawn.users.list"}).Error; err != nil {
		t.Fatal(err)
	}
	token := rand.Text()
	if err := a.DB.Create(&models.Session{ID: rand.Text(), UserID: user.ID, AccessHash: admin.TokenHash(token), RefreshHash: rand.Text(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	staleActor, err := a.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ClearUserCaches(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	loaded, release := make(chan struct{}), make(chan struct{})
	releaseLoad := sync.OnceFunc(func() { close(release) })
	defer releaseLoad()
	var once sync.Once
	if err := a.DB.Callback().Query().After("gorm:after_query").Register("test:pause_user_cache", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table == "admin_users" {
			once.Do(func() { close(loaded); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	var previous *admin.Actor
	done := make(chan error, 1)
	go func() { actor, err := a.Authenticate(token); previous = actor; done <- err }()
	select {
	case <-loaded:
	case <-time.After(3 * time.Second):
		t.Fatal("资料查询未进入暂停点")
	}
	if err := a.DB.Model(&role).Update("status", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.ClearUserCaches(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	// 手动删除版本Key也不能让旧版本重新有效。
	if err := a.Redis.Del(context.Background(), a.Key(constants.ForeverUserCacheVersion, user.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Menus(staleActor, "zh"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []constants.Definition{constants.TempUserInfo, constants.TempUserPermissions, constants.TempUserMenus} {
		if store.Exists(a.Key(key, user.ID)) {
			t.Fatal("清理前的请求写回旧缓存", key.Name)
		}
	}
	releaseLoad()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if previous.Can("dawn.users.list") {
		t.Fatal("权限变更期间的请求未重新读取授权")
	}
	actor, err := a.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Can("dawn.users.list") || len(actor.User.Roles) != 0 {
		t.Fatal("停用角色仍然授权")
	}
	if err := a.DB.Model(&role).Update("status", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Redis.Del(context.Background(), a.Key(constants.ForeverUserCacheVersion, user.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	actor, err = a.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if !actor.Can("dawn.users.list") {
		t.Fatal("重建版本时未清除旧版本数据")
	}
}

func TestFrameworkRoutesAndErrorReporting(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "route:list --path=admin/users --json"); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range route.List() {
		found[r.URI] = true
	}
	for _, path := range []string{"/admin/users", "/admin/roles", "/admin/attachments", "/admin/code/previews"} {
		if !found[path] {
			t.Fatal("原生路由表缺少接口", path)
		}
	}
	if path, err := route.URL("dawn.posts.show", map[string]any{"id": 1}); err != nil || path != "/admin/posts/1" {
		t.Fatal("原生命名路由不可用", path, err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("test database unavailable")
	reports := 0
	h := exception.Resolve()
	h.Reporters = append(h.Reporters, func(_ any, err error, fields map[string]any) {
		if !errors.Is(err, sentinel) || fields["status"] != 500 {
			t.Errorf("框架未收到原始异常: %v %v", err, fields)
		}
		reports++
	})
	a.Router.GET("/test-error", func(c *gin.Context) { response.Report(c, sentinel) })
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("GET", "/test-error", nil))
	var body map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if out.Code != 500 || reports != 1 || body["message"] != "操作失败,请查看服务日志" || len(body) != 1 {
		t.Fatal("异常未统一上报或响应泄露", reports, out.Code, body)
	}
}
