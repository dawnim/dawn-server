package tests

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/controllers"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

type restRecord struct {
	models.Base
	Name      string  `json:"name" gorm:"not null" binding:"required"`
	Count     int64   `json:"count" gorm:"not null;default:7"`
	Active    bool    `json:"active" gorm:"not null;default:true"`
	Note      *string `json:"note"`
	CreatedBy uint    `json:"created_by" gorm:"not null"`
}

type restImportRecord struct {
	models.Base
	Code      string `json:"code" gorm:"not null;uniqueIndex" binding:"required"`
	Name      string `json:"name" gorm:"not null" binding:"required"`
	CreatedBy uint   `json:"created_by" gorm:"not null"`
}

func TestRESTResources(t *testing.T) {
	redis := miniredis.RunT(t)
	root := t.TempDir()
	driver, dsn := "sqlite", filepath.Join(root, "rest.sqlite")
	if os.Getenv("DAWN_TEST_DRIVER") != "" && os.Getenv("DAWN_TEST_DSN") != "" {
		driver, dsn = os.Getenv("DAWN_TEST_DRIVER"), os.Getenv("DAWN_TEST_DSN")
	}
	a, err := newTestApp(testOptions{AdminUsername: "rest_admin", AdminPassword: "test-password", DBDriver: driver, DBDSN: dsn, Storage: root, RedisURL: "redis://" + redis.Addr()})
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
	var super models.AdminUser
	if err := a.DB.Where("username = ?", "rest_admin").First(&super).Error; err != nil {
		t.Fatal(err)
	}
	session := func(userID uint) string {
		_, token := testSession(t, a, userID)
		return token
	}
	token := session(super.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AutoMigrate(&restRecord{}, &restImportRecord{}); err != nil {
		t.Fatal(err)
	}
	router := route.New()
	controllers.RegisterResource(a, router.Prefix("/admin"), controllers.Resource[restImportRecord]{
		Path: "/import-records", Permission: "dawn.posts", Name: "app.import-records", Fields: []string{"name", "code", "created_by"}, Importable: true,
		Scope: func(db *gorm.DB, actor *admin.Actor) *gorm.DB {
			return db.Where("created_by = ?", actor.User.ID)
		},
		Prepare: func(row *restImportRecord, actor *admin.Actor, editing bool) {
			if !editing {
				row.CreatedBy = actor.User.ID
			}
		},
	})
	controllers.RegisterResource(a, router.Prefix("/admin"), controllers.Resource[restRecord]{
		Path: "/rest-records", Permission: "dawn.posts", Name: "app.rest-records", Fields: []string{"name", "count", "active", "note", "created_by"}, Required: []string{"name"},
		Scope: func(db *gorm.DB, actor *admin.Actor) *gorm.DB {
			if actor.User.Super {
				return db
			}
			return db.Where("created_by = ?", actor.User.ID)
		},
		Prepare: func(row *restRecord, actor *admin.Actor, editing bool) {
			if !editing {
				row.CreatedBy = actor.User.ID
			}
		},
	})
	readOnly := func(c *gin.Context) {
		c.JSON(200, gin.H{"data": gin.H{"id": c.Param("id"), "group": c.GetBool("resource_group")}})
	}
	controllers.RegisterResourceRoutes(a, router.Prefix("/admin/isolated").Name("qa.").Middleware(func(c *gin.Context) {
		c.Set("resource_group", true)
		c.Next()
	}), "/records", "dawn.posts", controllers.ResourceHandlers{Name: "app.records", Index: readOnly, Show: readOnly})
	for name, want := range map[string]string{
		"app.rest-records.show":         "/admin/rest-records/7",
		"qa.app.records.show":           "/admin/isolated/records/7",
		"qa.app.records.index":          "/admin/isolated/records",
		"app.rest-records.destroy-many": "/admin/rest-records",
	} {
		if path, err := router.URL(name, map[string]any{"id": 7}); err != nil || path != want {
			t.Fatal("资源命名路由或分组丢失", name, path, err)
		}
	}
	if err := router.Mount(a.Router); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, access string, input any) (int, map[string]any, *httptest.ResponseRecorder) {
		t.Helper()
		var body bytes.Buffer
		if input != nil {
			if err := json.NewEncoder(&body).Encode(input); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "/admin"+path, &body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		result := map[string]any{}
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err, res.Body.String())
			}
		}
		return res.Code, result, res
	}
	status, result, res := call("POST", "/rest-records", token, gin.H{"name": "first", "count": 0, "active": false, "note": "text"})
	if status != 201 {
		t.Fatal(status, result)
	}
	row := result["data"].(map[string]any)
	id := uint(row["id"].(float64))
	if row["count"] != float64(0) || row["active"] != false || row["created_by"] != float64(super.ID) || res.Header().Get("Location") != "/admin/rest-records/"+strconv.Itoa(int(id)) {
		t.Fatal("新增协议或零值不正确", result, res.Header())
	}
	status, result, _ = call("PATCH", fmt.Sprintf("/rest-records/%d", id), token, gin.H{"count": 0, "active": false, "note": nil})
	if status != 200 {
		t.Fatal(status, result)
	}
	row = result["data"].(map[string]any)
	if row["name"] != "first" || row["note"] != nil || row["active"] != false || row["count"] != float64(0) {
		t.Fatal("PATCH丢失缺省字段或零值", result)
	}
	status, result, _ = call("PUT", fmt.Sprintf("/rest-records/%d", id), token, gin.H{"name": "replaced"})
	row = result["data"].(map[string]any)
	if status != 200 || row["name"] != "replaced" || row["count"] != float64(7) || row["active"] != true || row["note"] != nil || row["created_by"] != float64(super.ID) {
		t.Fatal("PUT未完整替换可写字段或改动归属", status, result)
	}
	if status, _, _ := call("PUT", fmt.Sprintf("/rest-records/%d", id), token, gin.H{"count": 8}); status != 422 {
		t.Fatal("PUT漏掉必填校验", status)
	}
	status, result, _ = call("GET", fmt.Sprintf("/rest-records/%d", id), token, nil)
	if status != 200 || result["data"].(map[string]any)["count"] != float64(7) {
		t.Fatal("PUT校验失败却写入", status, result)
	}
	for _, input := range []gin.H{{"id": id}, {"created_by": 999}, {"count": nil}, {"unknown": 1}} {
		if status, result, _ := call("PATCH", fmt.Sprintf("/rest-records/%d", id), token, input); status != 422 {
			t.Fatal("接受只读字段或非法null", status, result)
		}
	}
	if status, result, _ := call("POST", "/rest-records", token, gin.H{"name": "default"}); status != 201 || result["data"].(map[string]any)["count"] != float64(7) || result["data"].(map[string]any)["active"] != true {
		t.Fatal("数据库默认值未保留", status, result)
	}
	if status, _, _ := call("POST", "/rest-records", token, gin.H{"count": 1}); status != 422 {
		t.Fatal("漏掉必填字段", status)
	}
	status, result, _ = call("POST", "/users", token, gin.H{"username": "replace_user", "password": "test-password", "realname": "旧名称", "status": 2})
	if status != 201 {
		t.Fatal(status, result)
	}
	userID := result["data"].(map[string]any)["id"]
	status, result, _ = call("PUT", fmt.Sprintf("/users/%.0f", userID), token, gin.H{"username": "replace_user"})
	if status != 200 || result["data"].(map[string]any)["realname"] != "" || result["data"].(map[string]any)["status"] != float64(1) {
		t.Fatal("用户PUT未恢复默认值", status, result)
	}
	status, result, _ = call("POST", "/roles", token, gin.H{"name": "替换角色", "code": "replace_role", "department_ids": []uint{1}, "data_scope": "departments"})
	if status != 201 {
		t.Fatal(status, result)
	}
	roleID := result["data"].(map[string]any)["id"]
	status, result, _ = call("PUT", fmt.Sprintf("/roles/%.0f", roleID), token, gin.H{"name": "替换角色", "code": "replace_role"})
	if status != 200 || result["data"].(map[string]any)["data_scope"] != "self" || len(result["data"].(map[string]any)["department_ids"].([]any)) != 0 {
		t.Fatal("PUT未恢复JSON默认值", status, result)
	}
	posts := make([]models.Post, 237)
	for i := range posts {
		posts[i] = models.Post{Name: fmt.Sprintf("rest_%03d", i), Code: fmt.Sprintf("rest_%03d", i), Status: 1}
	}
	if err := a.DB.CreateInBatches(&posts, 50).Error; err != nil {
		t.Fatal(err)
	}
	status, result, _ = call("GET", "/posts?name=rest_&page=2&page_size=100&sort_by=id&sort_order=asc", token, nil)
	if status != 200 || len(result["data"].([]any)) != 100 || result["meta"].(map[string]any)["total"] != float64(237) || result["data"].([]any)[0].(map[string]any)["id"] != float64(posts[100].ID) {
		t.Fatal("跨页查询不正确", status, result)
	}
	status, result, _ = call("GET", "/posts?name=rest_&page_size=300", token, nil)
	if status != 200 || len(result["data"].([]any)) != 237 || result["meta"].(map[string]any)["page_size"] != float64(300) {
		t.Fatal("条数仍被固定上限截断", status, result)
	}
	status, result, _ = call("GET", "/posts?name=rest_&page=99&page_size=100", token, nil)
	if status != 200 || len(result["data"].([]any)) != 0 || result["meta"].(map[string]any)["total"] != float64(237) {
		t.Fatal("空页丢失总数", status, result)
	}
	if status, _, _ := call("DELETE", fmt.Sprintf("/posts?ids=%d,99999999", posts[0].ID), token, nil); status != 404 {
		t.Fatal("批量删除未整批校验", status)
	}
	var count int64
	a.DB.Model(&models.Post{}).Where("id = ?", posts[0].ID).Count(&count)
	if count != 1 {
		t.Fatal("批量失败仍删除了部分数据")
	}
	status, _, res = call("DELETE", fmt.Sprintf("/posts?ids=%d,%d", posts[0].ID, posts[1].ID), token, nil)
	if status != 204 || res.Body.Len() != 0 {
		t.Fatal("批量删除不是无正文204", status, res.Body.String())
	}
	if status, _, _ := call("DELETE", "/posts", token, nil); status != 422 {
		t.Fatal("允许无ID整集合删除", status)
	}
	if err := a.DB.Where("code LIKE ?", "rest_%").Delete(&models.Post{}).Error; err != nil {
		t.Fatal(err)
	}
	status, result, _ = call("GET", "/recycle-bin?resource=posts&page_size=300", token, nil)
	if status != 200 || len(result["data"].([]any)) != 237 || result["meta"].(map[string]any)["total"] != float64(237) {
		t.Fatal("回收站仍截断或假分页", status, result)
	}
	status, _, _ = call("POST", "/recycle-bin/restorations", token, gin.H{"resource": "posts", "ids": []uint{posts[0].ID, posts[1].ID}})
	if status != 204 {
		t.Fatal("批量恢复失败", status)
	}
	// Invalid rows roll back the entire import; exporting ignores list pagination.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, _ := form.CreateFormFile("file", "posts.csv")
	_, _ = io.WriteString(file, "name,code,status,sort\nrest_import,rest_import,1,0\nrest_invalid,"+posts[0].Code+",9,1\n")
	_ = form.Close()
	req := httptest.NewRequest("POST", "/admin/posts/imports", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != 422 {
		t.Fatal("导入非法行未拒绝", recorder.Code, recorder.Body.String())
	}
	a.DB.Model(&models.Post{}).Where("code = ?", "rest_import").Count(&count)
	if count != 0 {
		t.Fatal("导入部分提交")
	}
	req = httptest.NewRequest("GET", "/admin/posts?name=rest_&page_size=1", nil)
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	csvRows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil || recorder.Code != 200 || len(csvRows) != 3 {
		t.Fatal("导出受分页限制或漏掉筛选", recorder.Code, err, recorder.Body.String())
	}
	body.Reset()
	form = multipart.NewWriter(&body)
	file, _ = form.CreateFormFile("file", "defaults.csv")
	_, _ = io.WriteString(file, "name,code,sort\nrest_csvdefault,rest_csvdefault,0\n")
	_ = form.Close()
	req = httptest.NewRequest("POST", "/admin/posts/imports", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != 200 {
		t.Fatal("CSV未使用声明默认值", recorder.Code, recorder.Body.String())
	}
	var imported models.Post
	if err := a.DB.Where("code = ?", "rest_csvdefault").First(&imported).Error; err != nil || imported.Status != 1 || imported.Sort != 0 {
		t.Fatal("CSV默认值或零值错误", err, imported)
	}
	body.Reset()
	form = multipart.NewWriter(&body)
	file, _ = form.CreateFormFile("file", "import-template.csv")
	templateReq := httptest.NewRequest("GET", "/admin/posts/import-template", nil)
	templateReq.Header.Set("Authorization", "Bearer "+token)
	templateRes := httptest.NewRecorder()
	handler.ServeHTTP(templateRes, templateReq)
	if templateRes.Code != 200 {
		t.Fatal("下载岗位导入模板失败", templateRes.Code, templateRes.Body.String())
	}
	_, _ = io.Copy(file, templateRes.Body)
	_ = form.Close()
	req = httptest.NewRequest("POST", "/admin/posts/imports", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"count":0`) {
		t.Fatal("空导入模板未作为空导入处理", recorder.Code, recorder.Body.String())
	}
	imported.UpdatedAt = 1
	if err := a.DB.Model(&imported).UpdateColumns(map[string]any{"remark": "保留未导入列", "updated_at": imported.UpdatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	importCSV := func(path, access, csv, locale string) (int, map[string]any) {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("file", "posts.csv")
		_, _ = io.WriteString(file, csv)
		_ = form.Close()
		req := httptest.NewRequest("POST", "/admin"+path, &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Accept-Language", locale)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err, res.Body.String())
		}
		return res.Code, result
	}
	input := "name,code,status,sort\n\"已覆盖，含逗号\",rest_csvdefault,2,0\n新增,rest_csvnew,1,0\n最后一行,rest_csvnew,1,0\n"
	status, result = importCSV("/posts/imports?preview=true", token, input, "en")
	if status != 200 || len(result["data"].(map[string]any)["rows"].([]any)) != 3 || result["data"].(map[string]any)["columns"].([]any)[0].(map[string]any)["label"] != "Post name" {
		t.Fatal("导入预览内容或语言错误", status, result)
	}
	var unchanged models.Post
	a.DB.First(&unchanged, imported.ID)
	a.DB.Model(&models.Post{}).Where("code = ?", "rest_csvnew").Count(&count)
	if unchanged.Name != imported.Name || count != 0 {
		t.Fatal("预览写入了数据库", unchanged, count)
	}
	status, result = importCSV("/posts/imports", token, input, "zh")
	if status != 200 || result["data"].(map[string]any)["count"] != float64(1) || result["data"].(map[string]any)["updated"] != float64(2) {
		t.Fatal("覆盖导入计数错误", status, result)
	}
	var overwritten, added models.Post
	a.DB.First(&overwritten, imported.ID)
	a.DB.Where("code = ?", "rest_csvnew").First(&added)
	if overwritten.Name != "已覆盖，含逗号" || overwritten.Status != 2 || overwritten.Sort != 0 || overwritten.Remark != "保留未导入列" || overwritten.CreatedAt != imported.CreatedAt || overwritten.UpdatedAt <= imported.UpdatedAt || added.Name != "最后一行" || added.CreatedAt == 0 {
		t.Fatal("覆盖导入的时间、零值或最后一行错误", overwritten, added)
	}
	status, result = importCSV("/posts/imports", token, "name,code,status\n不应写入,rest_csvdefault,1\n无效,rest_csvinvalid,9\n", "en")
	a.DB.First(&unchanged, imported.ID)
	if status != 422 || result["message"] != "Import failed: Invalid or missing fields in CSV row 3" || unchanged.Name != overwritten.Name || unchanged.Status != overwritten.Status || unchanged.UpdatedAt != overwritten.UpdatedAt {
		t.Fatal("无效行未回滚之前的覆盖或错误未翻译", status, result, unchanged)
	}
	// Data scope applies to whole batches, with no deletion before permission checking.
	role := models.Role{Name: "scope", Code: "rest_scope", Level: 100, Status: 1, DataScope: models.ScopeSelf}
	if err := a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&[]models.RolePermission{{RoleID: role.ID, Key: "dawn.posts.list"}, {RoleID: role.ID, Key: "dawn.posts.delete"}}).Error; err != nil {
		t.Fatal(err)
	}
	operator := models.AdminUser{Username: "rest_operator", Status: 1, Roles: []models.Role{role}}
	if err := a.DB.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	own := restRecord{Name: "owned", CreatedBy: operator.ID}
	if err := a.DB.Create(&own).Error; err != nil {
		t.Fatal(err)
	}
	access := session(operator.ID)
	if status, _, _ := call("GET", "/isolated/records", "", nil); status != 401 {
		t.Fatal("独立分组未校验登录", status)
	}
	status, result, _ = call("GET", "/isolated/records/7", access, nil)
	if status != 200 || result["data"].(map[string]any)["id"] != "7" || result["data"].(map[string]any)["group"] != true {
		t.Fatal("独立分组未保留id、权限或中间件", status, result)
	}
	for method, path := range map[string]string{"POST": "/isolated/records", "PATCH": "/isolated/records/7", "PUT": "/isolated/records/7", "DELETE": "/isolated/records/7"} {
		if status, _, _ := call(method, path, token, gin.H{}); status != 405 {
			t.Fatal("只读资源注册了未提供的操作", method, status)
		}
	}
	for method, path := range map[string]string{"POST": "/rest-records", "PATCH": fmt.Sprintf("/rest-records/%d", own.ID), "PUT": fmt.Sprintf("/rest-records/%d", own.ID)} {
		if status, _, _ := call(method, path, access, gin.H{}); status != 403 {
			t.Fatal("资源写入未校验对应操作权限", method, status)
		}
	}
	status, result, _ = call("GET", "/rest-records", access, nil)
	if status != 200 || result["meta"].(map[string]any)["total"] != float64(1) {
		t.Fatal("读取超出数据范围", status, result)
	}
	if status, _, _ := call("DELETE", fmt.Sprintf("/rest-records?ids=%d,%d", own.ID, id), access, nil); status != 404 {
		t.Fatal("批量删除绕过数据范围", status)
	}
	a.DB.Model(&restRecord{}).Where("id = ?", own.ID).Count(&count)
	if count != 1 {
		t.Fatal("越权批量请求仍删除了自己的记录")
	}
	if err := a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: "dawn.posts.create"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.ClearUserCaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, result = importCSV("/posts/imports", access, "name,code\n不应更新,rest_csvdefault\n", "zh")
	if status != 422 || result["message"] != "导入失败: 第2行记录已存在，没有更新权限" {
		t.Fatal("覆盖导入绕过更新权限", status, result)
	}
	if err := a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: "dawn.posts.update"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.ClearUserCaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []uint{super.ID, operator.ID} {
		if err := a.DB.Create(&restImportRecord{Code: fmt.Sprint(owner), Name: "原始", CreatedBy: owner}).Error; err != nil {
			t.Fatal(err)
		}
	}
	status, result = importCSV("/import-records/imports", access, "name,code\n越权覆盖,"+fmt.Sprint(super.ID)+"\n", "zh")
	if status != 422 || result["message"] != "导入失败: 第2行存在重复数据或无权更新" {
		t.Fatal("覆盖导入绕过数据范围", status, result)
	}
	status, result = importCSV("/import-records/imports", access, "name,code\n自己的记录,"+fmt.Sprint(operator.ID)+"\n", "zh")
	if status != 200 || result["data"].(map[string]any)["updated"] != float64(1) {
		t.Fatal("自身范围内覆盖失败", status, result)
	}
}
