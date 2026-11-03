package tests

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

func TestUserManagement(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	departments := []models.Department{
		{Base: models.Base{ID: 10}, Name: "部门", Status: 1},
		{Base: models.Base{ID: 11}, Name: "子部门", ParentID: 10, Status: 1},
		{Base: models.Base{ID: 12}, Name: "孙部门", ParentID: 11, Status: 1},
		{Base: models.Base{ID: 20}, Name: "其他部门", Status: 1},
	}
	if err := a.DB.Create(&departments).Error; err != nil {
		t.Fatal(err)
	}
	roles := []models.Role{
		{Name: "账号维护", Code: "account_manager", Level: 90, Status: 1, DataScope: models.ScopeDepartments, DepartmentIDs: []uint{10}},
		{Name: "业务负责人", Code: "business", Level: 80, Status: 1, DataScope: models.ScopeAll},
		{Name: "普通员工", Code: "employee", Level: 10, Status: 1, DataScope: models.ScopeSelf},
	}
	for i := range roles {
		if err := a.DB.Create(&roles[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"dawn.users.list", "dawn.users.create", "dawn.users.update", "dawn.users.delete", "dawn.users.password", "dawn.users.dashboard", "dawn.users.cache", "dawn.roles.list", "dawn.roles.create", "dawn.roles.update", "dawn.roles.delete", "dawn.roles.permissions"} {
		if err := a.DB.Create(&models.RolePermission{RoleID: roles[0].ID, Key: key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DB.Create(&models.RolePermission{RoleID: roles[1].ID, Key: "dawn.settings.update"}).Error; err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
	users := []models.User{
		{Username: "manager", Password: string(hash), Status: 1, DepartmentID: 20, Roles: []models.Role{roles[0]}},
		{Username: "worker", Password: string(hash), Status: 1, DepartmentID: 12, Roles: []models.Role{roles[2]}},
		{Username: "peer", Password: string(hash), Status: 1, DepartmentID: 11, Roles: []models.Role{roles[0]}},
		{Username: "outside", Password: string(hash), Status: 1, DepartmentID: 20, Roles: []models.Role{roles[2]}},
	}
	for i := range users {
		if err := a.DB.Create(&users[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	session := func(id uint) string {
		t.Helper()
		token := rand.Text()
		if err := a.DB.Create(&models.Session{ID: rand.Text(), UserID: id, AccessHash: admin.TokenHash(token), RefreshHash: admin.TokenHash(rand.Text()), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
		return token
	}
	root, manager, worker := session(1), session(users[0].ID), session(users[1].ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token string, input any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var data map[string]any
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
				t.Fatal(res.Body.String())
			}
		}
		return res.Code, data
	}

	status, result := call("GET", "/users", manager, nil)
	if status != 200 || int(result["meta"].(map[string]any)["total"].(float64)) != 5 {
		t.Fatal("管理员目录不完整", status, result)
	}
	for _, row := range result["data"].([]any) {
		user := row.(map[string]any)
		want := user["username"] == "worker"
		if user["can_manage"] != want {
			t.Fatal("管理范围或级别错误", user)
		}
	}
	if status, result := call("GET", "/roles", manager, nil); status != 200 || len(result["data"].([]any)) != 3 {
		t.Fatal("角色目录不完整", status, result)
	}
	status, result = call("GET", "/roles/options", manager, nil)
	if status != 200 || len(result["data"].([]any)) != 2 {
		t.Fatal("下级角色选项受业务权限或数据范围限制", status, result)
	}
	status, result = call("POST", "/users", manager, gin.H{"username": "new_leader", "password": "123456", "status": 1, "dept_id": 12, "role_ids": []uint{roles[1].ID}})
	if status != 201 {
		t.Fatal("不能给子部门新员工分配下级业务角色", status, result)
	}
	for _, id := range []uint{1, users[0].ID, users[2].ID, users[3].ID} {
		for _, item := range []struct {
			method, suffix string
			input          any
		}{
			{"PATCH", "", gin.H{"realname": "不应修改"}},
			{"DELETE", "", nil},
			{"PUT", "/password", gin.H{"password": "654321"}},
			{"PUT", "/dashboard", gin.H{"dashboard": "work"}},
			{"DELETE", "/cache", nil},
		} {
			status, result := call(item.method, fmt.Sprintf("/users/%d%s", id, item.suffix), manager, item.input)
			if status != 403 && status != 404 {
				t.Fatal("只读账号允许写入", id, item, status, result)
			}
		}
	}
	for _, role := range []models.Role{roles[0], {Base: models.Base{ID: roles[1].ID}, Level: 90}} {
		status, result := call("PATCH", fmt.Sprintf("/roles/%d", role.ID), manager, gin.H{"level": role.Level, "name": "越权"})
		if status != 404 && status != 422 && status != 403 {
			t.Fatal("角色原级别或新级别检查失效", status, result)
		}
	}
	if status, result := call("PUT", fmt.Sprintf("/roles/%d/permissions", roles[0].ID), manager, gin.H{"keys": []string{}}); status != 403 {
		t.Fatal("允许修改自身角色授权", status, result)
	}
	if status, result := call("PUT", fmt.Sprintf("/roles/%d/permissions", roles[1].ID), manager, gin.H{"keys": []string{"dawn.settings.update"}}); status != 200 {
		t.Fatal("下级授权错误地受自身业务权限限制", status, result)
	}

	call("GET", "/auth/me", worker, nil)
	call("GET", "/navigation", worker, nil)
	for _, definition := range []constants.Definition{constants.TempUserInfo, constants.TempUserPermissions, constants.TempUserMenus} {
		key := a.Key(definition, users[1].ID)
		if !store.Exists(key) || store.TTL(key) != definition.TTL {
			t.Fatal("未按用户缓存或TTL错误", key, store.TTL(key))
		}
	}
	info, _ := store.Get(a.Key(constants.TempUserInfo, users[1].ID))
	if strings.Contains(info, string(hash)) || strings.Contains(info, "password") {
		t.Fatal("资料缓存包含密码哈希")
	}
	wsServer := httptest.NewServer(a.Realtime)
	defer wsServer.Close()
	status, result = call("POST", "/realtime/ticket", worker, gin.H{})
	if status != 200 {
		t.Fatal(status, result)
	}
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.WriteJSON(realtime.Message{Type: "auth", Ticket: result["data"].(map[string]any)["ticket"].(string)}); err != nil {
		t.Fatal(err)
	}
	socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	var notification realtime.Message
	if err := socket.ReadJSON(&notification); err != nil || notification.Type != "ready" {
		t.Fatal("WS未连接", notification, err)
	}
	if status, result := call("PUT", fmt.Sprintf("/users/%d/dashboard", users[1].ID), manager, gin.H{"dashboard": "work"}); status != 204 {
		t.Fatal(status, result)
	}
	if err := socket.ReadJSON(&notification); err != nil || notification.Type != "user.refresh" {
		t.Fatal("设置首页未通知在线端", notification, err)
	}
	if err := socket.WriteJSON(realtime.Message{Type: "ack", ID: notification.ID, Channel: notification.Channel}); err != nil {
		t.Fatal(err)
	}
	status, result = call("GET", "/auth/me", worker, nil)
	if status != 200 || result["data"].(map[string]any)["dashboard"] != "work" {
		t.Fatal("工作台设置未保存或缓存未失效", status, result)
	}
	if status, result := call("PATCH", fmt.Sprintf("/users/%d", users[1].ID), manager, gin.H{"realname": "新名字"}); status != 200 {
		t.Fatal(status, result)
	}
	status, result = call("GET", "/auth/me", worker, nil)
	if status != 200 || result["data"].(map[string]any)["realname"] != "新名字" {
		t.Fatal("修改资料后缓存未失效", status, result)
	}
	call("GET", "/navigation", worker, nil)
	if status, result := call("DELETE", fmt.Sprintf("/users/%d/cache", users[1].ID), manager, nil); status != 204 {
		t.Fatal(status, result)
	}
	if err := socket.ReadJSON(&notification); err != nil || notification.Type != "user.refresh" {
		t.Fatal("清缓存未通知在线端", notification, err)
	}
	for _, definition := range []constants.Definition{constants.TempUserInfo, constants.TempUserPermissions, constants.TempUserMenus} {
		if store.Exists(a.Key(definition, users[1].ID)) {
			t.Fatal("用户缓存未清理", definition.Name)
		}
	}
	if status, result := call("GET", "/auth/me", worker, nil); status != 200 {
		t.Fatal("清缓存导致登录失效", status, result)
	}
	if status, result := call("PUT", fmt.Sprintf("/users/%d/password", users[1].ID), manager, gin.H{"password": "654321"}); status != 204 {
		t.Fatal(status, result)
	}
	if status, _ := call("GET", "/auth/me", worker, nil); status != 401 {
		t.Fatal("重置密码后旧会话仍有效", status)
	}
	if status, result := call("GET", "/users", root, nil); status != 200 {
		t.Fatal(status, result)
	}
}
