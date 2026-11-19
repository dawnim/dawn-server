package tests

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/models"
)

// 实际编译、启动两种生成模式，验证它们遵守相同的REST协议。
func TestGeneratedREST(t *testing.T) {
	redis := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: filepath.Join(t.TempDir(), "source.sqlite"), Storage: t.TempDir(), RedisURL: "redis://" + redis.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec("CREATE TABLE qa_generated_rest (id INTEGER PRIMARY KEY AUTOINCREMENT, name VARCHAR(80) NOT NULL, code VARCHAR(64) NOT NULL UNIQUE, count INT NOT NULL DEFAULT 7, active BOOLEAN NOT NULL DEFAULT TRUE, note TEXT, created_by BIGINT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, deleted_at BIGINT NOT NULL DEFAULT 0)").Error; err != nil {
		t.Fatal(err)
	}
	user := models.AdminUser{Username: "generator_rest", Super: true, Status: 1}
	if err := a.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	_, token := testSession(t, a, user.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile("application_test.go")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile("../go.sum")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"simple", "full"} {
		t.Run(mode, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/admin/code/previews?table=qa_generated_rest&resource=widgets&directory=crm&migration=1&mode="+mode, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != 200 {
				t.Fatal(res.Code, res.Body.String())
			}
			var result struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data["admin/src/api/crm/qa_generated_rest.ts"] == "" || !strings.Contains(result.Data["admin/src/views/crm/qa_generated_rest/index.vue"], "../../../api/crm/qa_generated_rest") {
				t.Fatal("自定义前端目录未同步文件与引用")
			}
			root := t.TempDir()
			if output := os.Getenv("DAWN_GENERATED_OUTPUT"); output != "" {
				root = filepath.Join(output, mode)
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for name, code := range result.Data {
				if strings.HasSuffix(name, ".md") {
					t.Fatal("生成结果包含说明Markdown", name)
				}
				if strings.Contains(code, "@@") || (strings.HasSuffix(name, ".go") && strings.Contains(code, "{{")) {
					t.Fatal("模板占位符未替换", name)
				}
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(code), 0600); err != nil {
					t.Fatal(err)
				}
			}
			module := fmt.Sprintf("module example.com/test\n\ngo 1.26.0\n\nrequire github.com/dawnim/dawn-server v0.1.0\nreplace github.com/dawnim/dawn-server => %s\n", filepath.ToSlash(sourceRoot))
			for name, data := range map[string][]byte{"go.mod": []byte(module), "go.sum": sum, "application_test.go": helper, "generated_test.go": []byte(generatedRESTTest)} {
				if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
			command.Dir = root
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("生成代码未通过实际CRUD: %v\n%s", err, output)
			}
		})
	}
}

const generatedRESTTest = `package tests
import (
 "bytes";"encoding/json";"fmt";"net/http/httptest";"path/filepath";"testing";"strings";"mime/multipart"
 "github.com/alicebob/miniredis/v2"
 "github.com/dawnim/dawn-server/app/models"
 _ "example.com/test/database/migrations"
 "github.com/prismgo/framework/route"
 generated "example.com/test/app/admin/controllers"
)
func TestGeneratedResource(t *testing.T){
 redis:=miniredis.RunT(t)
 a,err:=newTestApp(testOptions{MigrationPaths: []string{"database/migrations"}, DBDSN:filepath.Join(t.TempDir(),"generated.sqlite"),Storage:t.TempDir(),RedisURL:"redis://"+redis.Addr()});if err!=nil{t.Fatal(err)};defer a.Close()
 if err:=databaseCommand(a, "migrate");err!=nil{t.Fatal(err)}
 user:=models.AdminUser{Username:"generated",Super:true,Status:1};if err:=a.DB.Create(&user).Error;err!=nil{t.Fatal(err)}
 _,token:=testSession(t,a,user.ID)
 handler,err:=a.Handler();if err!=nil{t.Fatal(err)};routes:=route.New();generated.RegisterQaGeneratedRest(a,routes.Prefix("/admin"));if path,err:=routes.URL("app.qa_generated_rest.show",map[string]any{"id":7});err!=nil||path!="/admin/widgets/7"{t.Fatal("生成的命名路由错误",path,err)};if err:=routes.Mount(a.Router);err!=nil{t.Fatal(err)}
 call:=func(method,path string,input any)(int,map[string]any){t.Helper();raw,_:=json.Marshal(input);req:=httptest.NewRequest(method,"/admin"+path,bytes.NewReader(raw));req.Header.Set("Authorization","Bearer "+token);req.Header.Set("Content-Type","application/json");res:=httptest.NewRecorder();handler.ServeHTTP(res,req);result:=map[string]any{};if res.Body.Len()>0{if err:=json.Unmarshal(res.Body.Bytes(),&result);err!=nil{t.Fatal(res.Code,res.Body.String())}};return res.Code,result}
 if status,_:=call("POST","/widgets",map[string]any{"code":"missing"});status!=422{t.Fatal("缺少必填字段",status)}
 for _,name:=range []string{"",strings.Repeat("x",81)} { if status,_:=call("POST","/widgets",map[string]any{"name":name,"code":"invalid"});status!=422{t.Fatal("字符串校验失效",status)} }
 status,result:=call("POST","/widgets",map[string]any{"name":"one","code":"one","count":0,"active":false,"note":nil});if status!=201{t.Fatal(status,result)}
 row:=result["data"].(map[string]any);id:=uint(row["id"].(float64));if row["count"]!=float64(0)||row["active"]!=false||row["created_by"]!=float64(user.ID){t.Fatal("零值或归属错误",result)}
 status,result=call("PATCH",fmt.Sprintf("/widgets/%d",id),map[string]any{"count":0,"active":false});if status!=200||result["data"].(map[string]any)["name"]!="one"{t.Fatal("部分修改丢失字段",status,result)}
 status,result=call("PUT",fmt.Sprintf("/widgets/%d",id),map[string]any{"name":"replaced","code":"one"});if status!=200{t.Fatal(status,result)};row=result["data"].(map[string]any);if row["name"]!="replaced"||row["count"]!=float64(7)||row["active"]!=true||row["created_by"]!=float64(user.ID){t.Fatal("PUT未完整替换",result)}
 if status,_:=call("PUT",fmt.Sprintf("/widgets/%d",id),map[string]any{"count":3});status!=422{t.Fatal("PUT缺少必填字段",status)}
 status,result=call("GET","/widgets?page_size=300",nil);if status!=200||result["meta"].(map[string]any)["total"]!=float64(1)||result["meta"].(map[string]any)["page_size"]!=float64(300){t.Fatal("分页不规范",status,result)}
 // CSV shares permissions, validation and write semantics with JSON CRUD.
 exported:=httptest.NewRecorder();req:=httptest.NewRequest("GET","/admin/widgets?name=replaced",nil);req.Header.Set("Authorization","Bearer "+token);req.Header.Set("Accept","text/csv");handler.ServeHTTP(exported,req);if exported.Code!=200||!strings.Contains(exported.Body.String(),"replaced"){t.Fatal("CSV导出错误",exported.Code,exported.Body.String())}
 importCSV:=func(csv string, preview bool) (int,string){var body bytes.Buffer;writer:=multipart.NewWriter(&body);file,_:=writer.CreateFormFile("file","widgets.csv");file.Write([]byte(csv));writer.Close();path:="/admin/widgets/imports";if preview{path+="?preview=true"};req:=httptest.NewRequest("POST",path,&body);req.Header.Set("Content-Type",writer.FormDataContentType());req.Header.Set("Authorization","Bearer "+token);res:=httptest.NewRecorder();handler.ServeHTTP(res,req);return res.Code,res.Body.String()}
 if status,body:=importCSV("name,code\nimported,imported\n",true);status!=200||!strings.Contains(body,"imported"){t.Fatal("CSV预览错误",status,body)}
 if status,body:=importCSV("name,code\nimported,imported\n",false);status!=200{t.Fatal("CSV导入错误",status,body)}
 if status,body:=importCSV("name,code\nupdated,imported\n",false);status!=200||!strings.Contains(body,` + "`" + `"updated":1` + "`" + `){t.Fatal("CSV覆盖错误",status,body)}
 if status,body:=importCSV("name,code\n"+strings.Repeat("x",81)+",bad\n",false);status!=422{t.Fatal("CSV校验错误",status,body)}
 limited:=models.AdminUser{Username:"scoped",Status:1};if err:=a.DB.Create(&limited).Error;err!=nil{t.Fatal(err)};_,limitedToken:=testSession(t,a,limited.ID);superToken:=token;token=limitedToken
 if status,_:=call("GET","/widgets",nil);status!=403{t.Fatal("未授权访问生成接口",status)}
 role:=models.Role{Name:"self",Code:"self",Status:1,DataScope:"self"};a.DB.Create(&role);a.DB.Create(&models.UserRole{UserID:limited.ID,RoleID:role.ID});for _,action:=range []string{"list","create","update","delete","export"}{a.DB.Create(&models.RolePermission{RoleID:role.ID,Key:"app.qa_generated_rest."+action})};if err:=a.ClearUserCaches(req.Context(),limited.ID);err!=nil{t.Fatal(err)}
 if status,result:=call("GET","/widgets",nil);status!=200||result["meta"].(map[string]any)["total"]!=float64(0){t.Fatal("列表未限制数据范围",status,result)}
 if status,_:=call("GET",fmt.Sprintf("/widgets/%d",id),nil);status!=404{t.Fatal("详情越权",status)}
 if status,_:=call("PATCH",fmt.Sprintf("/widgets/%d",id),map[string]any{"name":"forbidden"});status!=404{t.Fatal("修改越权",status)}
 token=superToken
 if status,_:=call("DELETE",fmt.Sprintf("/widgets?ids=%d,99999999",id),nil);status!=404{t.Fatal("批量未整批验证",status)}
 if status,_:=call("GET",fmt.Sprintf("/widgets/%d",id),nil);status!=200{t.Fatal("失败批量已删除记录",status)}
 if status,_:=call("DELETE",fmt.Sprintf("/widgets?ids=%d",id),nil);status!=204{t.Fatal("删除响应错误",status)}
 if status,_:=call("GET",fmt.Sprintf("/widgets/%d",id),nil);status!=404{t.Fatal("软删除错误",status)}
}
`
