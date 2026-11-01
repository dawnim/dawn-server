package tests

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"maps"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/gin-gonic/gin"
	fs "github.com/prismgo/framework/contracts/filesystem"
)

// TestApplication 测试真实路由、迁移幂等、授权撤销和文件内容校验.
func TestApplication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redis := miniredis.RunT(t)
	directory := t.TempDir()
	driver, dsn := "sqlite", filepath.Join(directory, "test.sqlite")+"?_foreign_keys=on"
	if os.Getenv("DAWN_TEST_DRIVER") != "" && os.Getenv("DAWN_TEST_DSN") != "" {
		driver, dsn = os.Getenv("DAWN_TEST_DRIVER"), os.Getenv("DAWN_TEST_DSN")
	}
	a, err := newTestApp(testOptions{LinkSystems: []admin.LinkSystem{{Key: "crm", Origin: "https://example.com", Secret: strings.Repeat("s", 32), Permissions: []string{"dawn.users.list"}}}, DBDriver: driver, DBDSN: dsn, Storage: directory, RedisURL: "redis://" + redis.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := migrations.Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAdmin("short", "12345"); err == nil {
		t.Fatal("接受了少于6位的密码")
	}
	if err := a.CreateAdmin("six_chars", "654321"); err != nil {
		t.Fatal("6位密码应当允许", err)
	}
	if err := a.CreateAdmin("root", "test-password"); err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token string, data any) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("%s %s %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Code, result
	}
	refreshTokens := map[string]string{}
	login := func(username string) string {
		t.Helper()
		status, result := call("GET", "/auth/captcha", "", nil)
		if status != 200 {
			t.Fatal(result)
		}
		id := result["data"].(map[string]any)["uuid"].(string)
		picture, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result["data"].(map[string]any)["image"].(string), "data:image/png;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := png.Decode(bytes.NewReader(picture))
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, alpha := decoded.At(0, 0).RGBA()
		if alpha != 65535 {
			t.Fatal("验证码背景应为不透明色")
		}
		answer, err := redis.Get(a.Key(constants.TempCaptcha, id))
		if err != nil {
			t.Fatal(err)
		}
		status, result = call("POST", "/auth/login", "", gin.H{"username": username, "password": "test-password", "uuid": id, "code": answer})
		if status != 200 {
			t.Fatal(result)
		}
		data := result["data"].(map[string]any)
		refreshTokens[username] = data["refresh_token"].(string)
		return data["access_token"].(string)
	}
	token := login("root")
	status, result := call("GET", "/menus", token, nil)
	if status != 200 {
		t.Fatal(result)
	}
	dashboard := result["data"].([]any)[0].(map[string]any)
	if dashboard["key"] != "dawn.dashboard" || dashboard["title"] != "仪表盘" {
		t.Fatal("仪表盘目录错误", dashboard)
	}
	workbench := dashboard["children"].([]any)[0].(map[string]any)
	if workbench["key"] != "dawn.home" || workbench["title"] != "工作台" || workbench["component"] != "/dashboard/console" {
		t.Fatal("工作台必须挂在仪表盘下面", workbench)
	}
	profile := dashboard["children"].([]any)[1].(map[string]any)
	if profile["key"] != "dawn.profile" || profile["parent_key"] != "dawn.dashboard" || profile["path"] != "/dashboard/user-center" || profile["hidden"] != true {
		t.Fatal("个人中心必须作为仪表盘下的隐藏路由返回", profile)
	}
	before, err := a.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	status, result = call("POST", "/auth/refresh", "", gin.H{"refresh_token": refreshTokens["root"]})
	if status != 200 {
		t.Fatal(result)
	}
	if status, _ := call("GET", "/auth/me", token, nil); status != 401 {
		t.Fatal("续期后旧访问凭证仍有效")
	}
	if status, _ := call("POST", "/auth/refresh", "", gin.H{"refresh_token": refreshTokens["root"]}); status != 401 {
		t.Fatal("刷新凭证可以重复使用")
	}
	token = result["data"].(map[string]any)["access_token"].(string)
	after, err := a.Authenticate(token)
	if err != nil || before.SessionID != after.SessionID {
		t.Fatal("续期改变了登录会话", err)
	}
	status, result = call("GET", "/auth/me", token, nil)
	if status != 200 || result["data"].(map[string]any)["is_super"] != true {
		t.Fatal(result)
	}
	var admin models.User
	if err := a.DB.First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if admin.CreatedAt < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("时间不是毫秒时间戳: %d", admin.CreatedAt)
	}
	status, result = call("POST", "/system/roles", token, gin.H{"name": "操作员", "code": "operator", "level": 100, "status": 1, "data_scope": "self", "created_at": 1})
	if status != 200 {
		t.Fatal(result)
	}
	roleID := uint(result["data"].(map[string]any)["id"].(float64))
	if roleID == 0 {
		t.Fatal("创建角色没有返回ID")
	}
	if result["data"].(map[string]any)["created_at"].(float64) < float64(time.Now().Add(-time.Minute).UnixMilli()) {
		t.Fatal("创建时间被客户端覆盖")
	}
	status, result = call("PUT", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, gin.H{"keys": []string{"dawn.users.list"}})
	if status != 200 {
		t.Fatal(result)
	}
	status, result = call("POST", "/system/users", token, gin.H{"username": "operator", "password": "test-password", "status": 1, "role_ids": []uint{roleID}})
	if status != 200 {
		t.Fatal(result)
	}
	operator := login("operator")
	if status, result := call("GET", "/system/users", operator, nil); status != 200 {
		t.Fatal(result)
	}
	if status, _ := call("GET", "/system/roles", operator, nil); status != 403 {
		t.Fatalf("越权读取角色: %d", status)
	}
	status, result = call("PUT", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, gin.H{"keys": []string{}})
	if status != 200 {
		t.Fatal(result)
	}
	if status, _ := call("GET", "/system/users", operator, nil); status != 403 {
		t.Fatalf("权限撤销未立即生效: %d", status)
	}
	link := gin.H{"status": 1, "type": models.MenuLink, "key": "custom.manual", "title": "文档", "parent_key": "dawn.system", "url": "https://example.com", "mode": "new_tab", "sort": 100}
	status, result = call("POST", "/system/menus", token, link)
	if status != 200 {
		t.Fatal(result)
	}
	linkID := uint(result["data"].(map[string]any)["id"].(float64))
	if status, _ := call("POST", "/system/menus", token, link); status != 422 {
		t.Fatalf("重复Key未拒绝: %d", status)
	}
	link["key"] = "custom.changed"
	if status, _ := call("PUT", fmt.Sprintf("/system/menus/%d", linkID), token, link); status != 422 {
		t.Fatalf("稳定Key可以改名: %d", status)
	}
	status, result = call("GET", "/menus", token, nil)
	if status != 200 || !strings.Contains(fmt.Sprint(result), "custom.manual") {
		t.Fatal(result)
	}
	menuIDs := map[string]uint{}
	for _, menu := range []gin.H{
		{"status": 1, "type": models.MenuDirectory, "key": "custom.resources", "title": "资源", "parent_key": "dawn.system"},
		{"status": 1, "type": models.MenuDirectory, "key": "custom.resources.docs", "title": "资料", "parent_key": "custom.resources"},
		{"status": 1, "type": models.MenuLink, "key": "custom.resources.manual", "title": "手册", "parent_key": "custom.resources.docs", "url": "/manual", "mode": "iframe"},
		{"status": 1, "type": models.MenuAction, "key": "custom.resources.export", "title": "导出手册", "parent_key": "custom.resources.manual", "url": "/unused", "mode": "iframe"},
		{"status": 1, "type": models.MenuAction, "key": "custom.users.import", "title": "导入用户", "parent_key": "dawn.users"},
	} {
		status, result := call("POST", "/system/menus", token, menu)
		if status != 200 {
			t.Fatal(menu, result)
		}
		data := result["data"].(map[string]any)
		menuIDs[menu["key"].(string)] = uint(data["id"].(float64))
		if menu["type"] == models.MenuAction && (data["url"] != "" || data["mode"] != "") {
			t.Fatal("按钮保留了页面属性", result)
		}
	}
	for _, menu := range []gin.H{
		{"status": 1, "type": 9, "key": "custom.invalid", "title": "无效类型"},
		{"key": "custom.invalid", "title": "缺少类型"},
		{"status": 1, "type": models.MenuAction, "key": "custom.invalid", "title": "顶级按钮"},
		{"status": 1, "type": models.MenuAction, "key": "custom.invalid", "title": "目录下的按钮", "parent_key": "custom.resources"},
		{"status": 1, "type": models.MenuLink, "key": "custom.invalid", "title": "页面下的页面", "parent_key": "custom.resources.manual", "url": "/manual", "mode": "iframe"},
		{"status": 1, "type": models.MenuLink, "key": "custom.invalid", "title": "缺少地址", "parent_key": "custom.resources", "mode": "iframe"},
	} {
		if status, result := call("POST", "/system/menus", token, menu); status != 422 {
			t.Fatal("接受了无效菜单", menu, status, result)
		}
	}
	for _, menu := range []gin.H{
		{"status": 1, "type": models.MenuDirectory, "key": "custom.resources", "title": "资源", "parent_key": "custom.resources.docs"},
		{"status": 1, "type": models.MenuLink, "key": "custom.resources", "title": "资源", "url": "/manual", "mode": "iframe"},
	} {
		if status, result := call("PUT", fmt.Sprintf("/system/menus/%d", menuIDs["custom.resources"]), token, menu); status != 422 {
			t.Fatal("接受了循环目录或与子项冲突的类型", menu, status, result)
		}
	}
	status, result = call("GET", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, nil)
	if status != 200 {
		t.Fatal(result)
	}
	permissionKeys := map[string]bool{}
	for _, p := range result["data"].(map[string]any)["permissions"].([]any) {
		permissionKeys[p.(map[string]any)["key"].(string)] = true
	}
	if !permissionKeys["custom.resources.manual"] || !permissionKeys["custom.resources.export"] || permissionKeys["custom.resources"] {
		t.Fatal("菜单或按钮授权列表不正确", permissionKeys)
	}
	if status, _ := call("PUT", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, gin.H{"keys": []string{"custom.resources"}}); status != 403 {
		t.Fatal("目录被作为操作权限授权")
	}
	status, result = call("PUT", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, gin.H{"keys": []string{"custom.resources.manual", "custom.resources.export"}})
	if status != 200 {
		t.Fatal(result)
	}
	status, result = call("GET", "/menus", operator, nil)
	if status != 200 || !strings.Contains(fmt.Sprint(result), "custom.resources.docs") || !strings.Contains(fmt.Sprint(result), "custom.resources.manual") || strings.Contains(fmt.Sprint(result), "custom.resources.export") {
		t.Fatal("目录未展开或按钮进入页面路由", result)
	}
	status, result = call("GET", "/auth/me", operator, nil)
	if status != 200 || !strings.Contains(fmt.Sprint(result["data"].(map[string]any)["buttons"]), "custom.resources.export") {
		t.Fatal("按钮权限未传给前端", result)
	}
	if status, _ := call("POST", "/links/custom.resources.export/launch", token, nil); status != 404 {
		t.Fatal("按钮被作为外链打开")
	}
	page := gin.H{"status": models.Enabled, "type": models.MenuPage, "key": "custom.resources.page", "title": "项目页面", "parent_key": "custom.resources.docs", "path": "/custom/page", "component": "/business/page", "hidden": true, "keep_alive": true, "fixed_tab": true, "full_page": true}
	status, result = call("POST", "/system/menus", token, page)
	if status != 200 {
		t.Fatal(result)
	}
	pageID := uint(result["data"].(map[string]any)["id"].(float64))
	status, result = call("GET", "/menus", token, nil)
	if status != 200 || !strings.Contains(fmt.Sprint(result), "component:/business/page") || !strings.Contains(fmt.Sprint(result), "full_page:true") {
		t.Fatal("页面路由或显示配置没有返回", result)
	}
	page["status"] = models.Disabled
	if status, result := call("PUT", fmt.Sprintf("/system/menus/%d", pageID), token, page); status != 200 {
		t.Fatal(result)
	}
	status, result = call("GET", "/menus", token, nil)
	if status != 200 || strings.Contains(fmt.Sprint(result), "custom.resources.page") {
		t.Fatal("停用页面仍然可见", result)
	}
	var button models.Menu
	if err := a.DB.First(&button, menuIDs["custom.resources.export"]).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&button).Update("status", models.Disabled).Error; err != nil {
		t.Fatal(err)
	}
	actor, err := a.Authenticate(operator)
	if err != nil || actor.Can("custom.resources.export") {
		t.Fatal("停用按钮后权限仍有效", err)
	}
	for _, path := range []string{"/dashboard/statistics", "/dashboard/daily", "/dashboard/monthly", "/database/table-info", "/database/columns?table=admin_menus"} {
		if status, result := call("GET", path, token, nil); status != 200 {
			t.Fatal(path, result)
		}
	}
	status, result = call("GET", "/system/logs?kind=login", operator, nil)
	if status != 200 {
		t.Fatal("个人中心日志读取失败", result)
	}
	for _, raw := range result["data"].(map[string]any)["data"].([]any) {
		if uint(raw.(map[string]any)["user_id"].(float64)) != actor.User.ID {
			t.Fatal("个人中心读取到其他管理员日志", raw)
		}
	}
	status, result = call("GET", "/system/logs?kind=login&self=1", token, nil)
	if status != 200 {
		t.Fatal(result)
	}
	for _, raw := range result["data"].(map[string]any)["data"].([]any) {
		if uint(raw.(map[string]any)["user_id"].(float64)) != after.User.ID {
			t.Fatal("超级管理员个人中心读取到其他账号日志", raw)
		}
	}
	status, result = call("PUT", fmt.Sprintf("/system/roles/%d/permissions", roleID), token, gin.H{"keys": []string{}})
	if status != 200 {
		t.Fatal(result)
	}
	status, result = call("GET", "/menus", operator, nil)
	if status != 200 || strings.Contains(fmt.Sprint(result), "custom.resources") {
		t.Fatal("菜单撤权后仍然可见", result)
	}
	status, result = call("PUT", "/system/settings", token, gin.H{"site.title": "新的标题", "mail.password": "secret"})
	if status != 200 {
		t.Fatal(result)
	}
	value, err := a.Setting(context.Background(), "mail.password")
	if err != nil || string(value) != `"secret"` {
		t.Fatalf("配置解密失败: %s %v", value, err)
	}
	status, result = call("GET", "/system/settings", token, nil)
	if status != 200 || strings.Contains(fmt.Sprint(result), "value:secret") {
		t.Fatal("保密配置泄露", result)
	}
	status, result = call("GET", "/site", "", nil)
	site := result["data"].(map[string]any)
	if status != 200 || len(site) != 8 || site["timezone"] != "UTC" || site["server_time"].(float64) <= 0 || site["title"] != "新的标题" || strings.Contains(fmt.Sprint(site), "secret") {
		t.Fatal("公共站点配置错误或泄露私有配置", result)
	}
	content := []byte("a real upload fixture")
	md5Value := md5.Sum(content)
	shaValue := sha256.Sum256(content)
	upload := gin.H{"name": "file.txt", "size": len(content), "md5": hex.EncodeToString(md5Value[:]), "sha256": hex.EncodeToString(shaValue[:])}
	status, result = call("POST", "/attachments/check", token, upload)
	if status != 200 || result["data"].(map[string]any)["exists"] != false {
		t.Fatal(result)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"md5", "sha256"} {
		if err := writer.WriteField(field, upload[field].(string)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/admin/attachments/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	status, result = call("GET", "/attachments?mime=image/", token, nil)
	if status != 200 || result["data"].(map[string]any)["total"] != float64(0) {
		t.Fatal("图片选择器返回非图片文件", result)
	}
	var storedFile models.File
	if err := a.DB.First(&storedFile).Error; err != nil {
		t.Fatal(err)
	}
	if storedFile.Location.Driver != "local" || storedFile.Location.Root == "" {
		t.Fatal("附件未记录存储位置")
	}
	if status, result := call("PUT", "/system/settings", token, gin.H{"upload.driver": "invalid"}); status != 422 {
		t.Fatal("接受了未知上传驱动", result)
	}
	if status, result := call("PUT", "/system/settings", token, gin.H{"upload.local.root": filepath.Join(directory, "new-uploads")}); status != 200 {
		t.Fatal(result)
	}
	storedDisk, _, err := a.FileDisk(context.Background(), &storedFile.Location)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := storedDisk.Get(context.Background(), storedFile.Path); err != nil || string(data) != string(content) {
		t.Fatal("修改存储配置后旧文件不可读取", err)
	}
	if closer, ok := storedDisk.(interface{ Close() error }); ok {
		closer.Close()
	}
	status, result = call("POST", "/attachments/check", token, upload)
	if status != 200 || result["data"].(map[string]any)["exists"] != true {
		t.Fatal(result)
	}
	var beforeCount, afterCount int64
	a.DB.Model(&models.Attachment{}).Where("scope = ? AND file_id = ?", "admin", storedFile.ID).Count(&beforeCount)
	originalID := result["data"].(map[string]any)["id"]
	for range 3 {
		status, result = call("POST", "/attachments/check", token, upload)
		if status != 200 || result["data"].(map[string]any)["id"] != originalID {
			t.Fatal("重复上传未返回相同附件", result)
		}
	}
	a.DB.Model(&models.Attachment{}).Where("scope = ? AND file_id = ?", "admin", storedFile.ID).Count(&afterCount)
	if beforeCount != 1 || afterCount != 1 {
		t.Fatal("后台附件未按文件去重", beforeCount, afterCount)
	}

	if status, result := call("POST", "/attachments/check", operator, upload); status != 403 {
		t.Fatal("低权限用户可以查询文件", result)
	}
	for _, change := range []gin.H{{"name": "file.exe"}, {"category_id": 999999}} {
		input := maps.Clone(upload)
		maps.Copy(input, change)
		if status, result := call("POST", "/attachments/check", token, input); status != 422 {
			t.Fatal("秒传绕过了扩展名或分类限制", result)
		}
	}
	if status, result := call("PUT", "/system/settings", token, gin.H{"upload.max_size": len(content) - 1}); status != 200 {
		t.Fatal(result)
	}
	if status, result := call("POST", "/attachments/check", token, upload); status != 422 {
		t.Fatal("秒传绕过了大小限制", result)
	}
	if status, result := call("PUT", "/system/settings", token, gin.H{"upload.max_size": 64 << 20}); status != 200 {
		t.Fatal(result)
	}
	var logo bytes.Buffer
	if err := png.Encode(&logo, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	disk, location, err := a.FileDisk(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.PutReader(context.Background(), "logo.png", bytes.NewReader(logo.Bytes()), fs.PutOptions{ContentType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	logoMD5, logoSHA := md5.Sum(logo.Bytes()), sha256.Sum256(logo.Bytes())
	logoFile := models.File{Location: location, Path: "logo.png", MIME: "image/png", Size: int64(logo.Len()), MD5: hex.EncodeToString(logoMD5[:]), SHA256: hex.EncodeToString(logoSHA[:])}
	if err := a.DB.Create(&logoFile).Error; err != nil {
		t.Fatal(err)
	}
	logoAttachment := models.Attachment{Name: "logo.png", FileID: logoFile.ID}
	if err := a.DB.Create(&logoAttachment).Error; err != nil {
		t.Fatal(err)
	}
	logoURL := fmt.Sprintf("/admin/attachments/%d/content", logoAttachment.ID)
	if status, result := call("PUT", "/system/settings", token, gin.H{"site.logo": logoURL}); status != 200 {
		t.Fatal(result)
	}
	logoResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoResponse, httptest.NewRequest("GET", "/admin/site/logo?file=999999", nil))
	if logoResponse.Code != 200 || !bytes.Equal(logoResponse.Body.Bytes(), logo.Bytes()) {
		t.Fatal("公开Logo未限定为配置中的图片", logoResponse.Code)
	}
	privateResponse := httptest.NewRecorder()
	handler.ServeHTTP(privateResponse, httptest.NewRequest("GET", logoURL, nil))
	if privateResponse.Code != 401 {
		t.Fatal("普通附件路径允许匿名访问", privateResponse.Code)
	}

	status, result = call("POST", "/attachments/uploads", token, upload)
	if status != 200 {
		t.Fatal(result)
	}
	uploadID := result["data"].(map[string]any)["upload_id"].(string)
	chunkRequest := httptest.NewRequest("PUT", "/admin/attachments/uploads/"+uploadID+"/0", bytes.NewReader(content))
	chunkRequest.Header.Set("Authorization", "Bearer "+token)
	chunkResponse := httptest.NewRecorder()
	handler.ServeHTTP(chunkResponse, chunkRequest)
	if chunkResponse.Code != 200 {
		t.Fatal(chunkResponse.Code, chunkResponse.Body.String())
	}
	status, result = call("POST", "/attachments/uploads/"+uploadID+"/complete", token, nil)
	if status != 200 {
		t.Fatal(result)
	}
	external := func(action string, data any) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/admin/integrations/"+action, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("crm", strings.Repeat("s", 32))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return res.Code, result
	}
	status, result = call("POST", "/system/menus", token, gin.H{"status": 1, "type": models.MenuLink, "key": "custom.crm", "title": "CRM", "url": "https://example.com/crm", "mode": "new_tab", "system_key": "crm"})
	if status != 200 {
		t.Fatal(result)
	}
	status, result = call("POST", "/links/custom.crm/launch", token, nil)
	if status != 200 {
		t.Fatal(result)
	}
	target, err := url.Parse(result["data"].(map[string]any)["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ticket := target.Query().Get("dawn_ticket")
	if ticket == "" {
		t.Fatal("受控外链没有票据")
	}
	status, result = external("exchange", gin.H{"ticket": ticket})
	if status != 200 {
		t.Fatal(result)
	}
	externalToken := result["data"].(map[string]any)["access_token"].(string)
	if status, _ := external("exchange", gin.H{"ticket": ticket}); status != 401 {
		t.Fatal("一次性票据可以重复使用")
	}
	if status, result := external("authorize", gin.H{"access_token": externalToken, "permission": "dawn.users.list"}); status != 200 || result["data"].(map[string]any)["allowed"] != true {
		t.Fatal(result)
	}
	if err := migrations.Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if status, result := call("POST", "/links/custom.manual/launch", token, nil); status != 200 {
		t.Fatal("重复迁移破坏外链", result)
	}

	for _, path := range []string{"/system/departments", "/system/posts", "/system/logs", "/system/sessions", "/cache/definitions", "/database/tables", "/database/columns?table=admin_users", "/database/recycle?resource=users", "/system/server"} {
		if status, result := call("GET", path, token, nil); status != 200 {
			t.Fatal(path, result)
		}
	}
	if status, result := call("GET", "/cache/instances?definition=ForeverSettings", token, nil); status != 200 {
		t.Fatal(result)
	}
	manualKey := a.Key(constants.TempWSTicket, "manual")
	if err := redis.Set(manualKey, "ticket"); err != nil {
		t.Fatal(err)
	}
	for _, access := range []struct {
		token  string
		status int
	}{{"", 401}, {operator, 403}, {token, 200}} {
		if status, result := call("DELETE", "/cache/value?definition=TempWSTicket&key="+url.QueryEscape(manualKey), access.token, nil); status != access.status {
			t.Fatal("手动清理未按删除权限控制", status, result)
		}
		if redis.Exists(manualKey) != (access.status != 200) {
			t.Fatal("手动清理的实际结果与权限不符")
		}
	}
	status, result = call("POST", "/system/posts", token, gin.H{"name": "测试岗位", "code": "tester", "sort": 100, "status": 1})
	if status != 200 {
		t.Fatal(result)
	}
	postID := uint(result["data"].(map[string]any)["id"].(float64))
	status, result = call("PUT", fmt.Sprintf("/system/posts/%d", postID), token, gin.H{"name": "更新岗位", "code": "tester", "sort": 101, "status": 1})
	if status != 200 {
		t.Fatal(result)
	}
	if status, result := call("GET", fmt.Sprintf("/system/posts/%d", postID), token, nil); status != 200 || result["data"].(map[string]any)["name"] != "更新岗位" {
		t.Fatal(result)
	}
	csvReq := httptest.NewRequest("POST", "/admin/system/posts/import", strings.NewReader("name,code,status,sort\n导入岗位,imported,1,100\n"))
	csvReq.Header.Set("Content-Type", "text/csv")
	csvReq.Header.Set("Authorization", "Bearer "+token)
	csvRes := httptest.NewRecorder()
	handler.ServeHTTP(csvRes, csvReq)
	if csvRes.Code != 200 {
		t.Fatal(csvRes.Code, csvRes.Body.String())
	}
	exportReq := httptest.NewRequest("GET", "/admin/system/posts/export", nil)
	exportReq.Header.Set("Authorization", "Bearer "+token)
	exportRes := httptest.NewRecorder()
	handler.ServeHTTP(exportRes, exportReq)
	if exportRes.Code != 200 || !strings.Contains(exportRes.Body.String(), "imported") {
		t.Fatal(exportRes.Body.String())
	}
	if status, result := call("DELETE", fmt.Sprintf("/system/posts/%d", postID), token, nil); status != 200 {
		t.Fatal(result)
	}
	if status, _ := call("GET", fmt.Sprintf("/system/posts/%d", postID), token, nil); status != 404 {
		t.Fatal("软删除记录仍可读取")
	}
	if status, result := call("POST", fmt.Sprintf("/database/recycle/%d/restore?resource=posts", postID), token, nil); status != 200 {
		t.Fatal(result)
	}
	if status, result := call("GET", fmt.Sprintf("/system/posts/%d", postID), token, nil); status != 200 {
		t.Fatal(result)
	}

	if status, result := call("POST", "/auth/logout", token, nil); status != 200 {
		t.Fatal(result)
	}
	if status, _ := external("authorize", gin.H{"access_token": externalToken}); status != 401 {
		t.Fatal("退出后外部系统仍可访问")
	}
	if status, _ := call("GET", "/auth/me", token, nil); status != 401 {
		t.Fatal("退出后凭证仍有效")
	}
}

func TestDataScope(t *testing.T) {
	actor := &admin.Actor{User: models.User{Base: models.Base{ID: 10}, DepartmentID: 3, Roles: []models.Role{{Status: models.Enabled, DataScope: models.ScopeDepartment}}}}
	redis := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + redis.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	type ScopedRecord struct {
		ID           uint
		CreatedBy    uint
		DepartmentID uint
		Status       int
	}
	if err := a.DB.AutoMigrate(&ScopedRecord{}); err != nil {
		t.Fatal(err)
	}
	rows := []ScopedRecord{{ID: 1, CreatedBy: 10, DepartmentID: 8, Status: 1}, {ID: 2, CreatedBy: 11, DepartmentID: 3, Status: 1}, {ID: 3, CreatedBy: 12, DepartmentID: 4, Status: 1}, {ID: 4, CreatedBy: 11, DepartmentID: 3, Status: 2}}
	if err := a.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var visible []ScopedRecord
	if err := a.DB.Where("status = ?", 1).Scopes(actor.Scope("created_by", "department_id")).Order("id").Find(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || visible[0].ID != 1 || visible[1].ID != 2 {
		t.Fatalf("本人/本部门权限范围错误: %#v", visible)
	}
	actor.User.DepartmentID = 0
	if err := a.DB.Create(&ScopedRecord{ID: 5, CreatedBy: 99, DepartmentID: 0, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Where("status = ?", 1).Scopes(actor.Scope("created_by", "department_id")).Find(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].CreatedBy != actor.User.ID {
		t.Fatalf("未设置部门时不能共享未归属数据: %#v", visible)
	}
}

func TestRoleLevel(t *testing.T) {
	actor := admin.Actor{User: models.User{Roles: []models.Role{{Status: models.Enabled, Level: 2000}, {Status: models.Disabled, Level: 1}}}}
	if actor.Level() != 2000 {
		t.Fatal("角色级别不应被固定上限截断", actor.Level())
	}
}
