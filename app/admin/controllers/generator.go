package controllers

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/route"
	"github.com/prismgo/framework/support"
)

//go:embed generator/*.tpl
var generatorTemplates embed.FS

var adminTables = []string{"admin_users", "admin_roles", "admin_user_roles", "admin_user_posts", "admin_role_permissions", "admin_departments", "admin_posts", "admin_sessions", "admin_settings", "admin_menus", "admin_files", "admin_attachments", "admin_categories", "admin_logs", "migrations", "sqlite_sequence"}

func (a *Controller) GeneratorRoutes(g *route.Registrar) {
	g.Get("/code/tables", middleware.Require(a.App, "dawn.generator.list"), func(c *gin.Context) {
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, slices.DeleteFunc(tables, func(table string) bool { return slices.Contains(adminTables, table) }))
	})

	generate := func(c *gin.Context) {
		download := strings.HasSuffix(c.FullPath(), "/archives")
		writing := c.Request.Method == http.MethodPost && !download
		if writing && (!middleware.Actor(c).User.Super || config.GetString("app.env") != "local") {
			response.Fail(c, 403, "仅本地环境的超级管理员可写入项目文件")
			return
		}
		var input struct {
			Files map[string]string `json:"files" binding:"required"`
		}
		if c.Request.Method == http.MethodPost {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
			if c.ShouldBindJSON(&input) != nil {
				response.Fail(c, 422, "参数格式错误")
				return
			}
		}
		table := c.Query("table")
		if !identifier.MatchString(table) {
			response.Fail(c, 422, "表名格式错误")
			return
		}
		if slices.Contains(adminTables, table) {
			response.Fail(c, 422, "不能生成扩展内置表的业务接口")
			return
		}
		if !a.DB.Migrator().HasTable(table) {
			response.Fail(c, 404, "数据表不存在")
			return
		}
		columns, err := a.DB.Migrator().ColumnTypes(table)
		if err != nil {
			response.Report(c, err)
			return
		}
		resource := c.DefaultQuery("resource", strings.ReplaceAll(table, "_", "-"))
		if !resourceName.MatchString(resource) {
			response.Fail(c, 422, "资源名格式错误")
			return
		}
		if slices.Contains([]string{"users", "roles", "departments", "posts", "menus", "navigation", "settings", "sessions", "logs", "mail-tests", "attachments", "attachment-checks", "uploads", "cache", "server", "tables", "table-names", "table-maintenance-runs", "recycle-bin", "code", "horizon", "auth", "links", "realtime"}, resource) {
			response.Fail(c, 422, "资源名与内置接口冲突")
			return
		}
		directory := c.DefaultQuery("directory", "business")
		if !frontendDirectory.MatchString(directory) {
			response.Fail(c, 422, "前端目录格式错误")
			return
		}
		name := goName(table)
		mode := c.DefaultQuery("mode", "simple")
		if mode != "simple" && mode != "full" {
			response.Fail(c, 422, "参数格式错误")
			return
		}
		var fields, selects, searchFields, tsFields, tsInputs, tsSearch, requiredFields strings.Builder
		definitions := []map[string]any{}
		translations := map[string]map[string]string{"zh": {}, "en": {}}
		search := []string{}
		defaults := map[string]any{}
		hasID, hasOwner, hasDepartment := false, false, false
		nullableFields := map[string]bool{}
		for _, column := range columns {
			columnName := column.Name()
			if !identifier.MatchString(columnName) {
				response.Fail(c, 422, "字段名不适合生成Go代码")
				return
			}
			hasOwner = hasOwner || columnName == "created_by"
			hasDepartment = hasDepartment || columnName == "department_id"
			if columnName == "id" {
				hasID, _ = column.PrimaryKey()
			}
			if columnName == "id" || columnName == "deleted_at" {
				continue
			}
			english := strings.ReplaceAll(columnName, "_", " ")
			english = strings.ToUpper(english[:1]) + english[1:]
			label, _ := column.Comment()
			if label == "" {
				label = columnName
				if columnName == "created_at" {
					label = "创建时间"
				} else if columnName == "updated_at" {
					label = "更新时间"
				}
			}
			translations["zh"][columnName], translations["en"][columnName] = label, english
			if columnName == "created_at" || columnName == "updated_at" {
				fmt.Fprintf(&tsFields, "  %s: number\n", columnName)
				definitions = append(definitions, map[string]any{"prop": columnName, "label": "fields." + table + "." + columnName, "type": "date", "readonly": true})
				continue
			}
			kind := columnGoType(column)
			sqlType := strings.ToUpper(column.DatabaseTypeName())
			nullable, _ := column.Nullable()
			nullableFields[columnName] = nullable
			fullType, _ := column.ColumnType()
			if fullType == "" {
				fullType = column.DatabaseTypeName()
			}
			tags := "column:" + columnName + ";type:" + fullType
			if !nullable {
				tags += ";not null"
			}
			if value, ok := column.DefaultValue(); ok {
				tags += ";default:" + strings.ReplaceAll(value, ";", `\;`)
			}
			if comment, _ := column.Comment(); comment != "" {
				tags += ";comment:" + strings.ReplaceAll(comment, ";", `\;`)
			}
			fmt.Fprintf(&fields, "\t%s %s `json:%s gorm:%s`\n", goName(columnName), kind, strconv.Quote(columnName), strconv.Quote(tags))
			tsType := "string"
			if strings.Contains(kind, "int") || strings.Contains(kind, "float") {
				tsType = "number"
			} else if strings.Contains(kind, "bool") {
				tsType = "boolean"
			}
			if nullable {
				tsType += " | null"
			}
			fmt.Fprintf(&tsFields, "  %s: %s\n", columnName, tsType)
			fmt.Fprintf(&selects, "%s,", strconv.Quote(columnName))
			inputType := "text"
			if strings.Contains(kind, "int") || strings.Contains(kind, "float") {
				inputType = "number"
			} else if strings.Contains(kind, "bool") {
				inputType = "boolean"
			}
			if strings.Contains(sqlType, "TEXT") || strings.Contains(sqlType, "JSON") {
				inputType = "textarea"
			}
			readonly := columnName == "created_by" || columnName == "department_id"
			definition := map[string]any{"prop": columnName, "label": "fields." + table + "." + columnName, "type": inputType, "readonly": readonly}
			defaultValue, hasDefault := column.DefaultValue()
			definition["required"] = !nullable && !hasDefault && !readonly
			if !readonly {
				optional := ""
				if nullable || hasDefault {
					optional = "?"
				} else {
					fmt.Fprintf(&requiredFields, "%q,", columnName)
				}
				fmt.Fprintf(&tsInputs, "  %s%s: %s\n", columnName, optional, tsType)
			}
			if length, ok := column.Length(); ok && length > 0 {
				definition["maxlength"] = length
			}
			definitions = append(definitions, definition)
			if !readonly {
				var value any = ""
				switch inputType {
				case "number":
					value = 0
					if hasDefault {
						if n, e := strconv.ParseFloat(defaultValue, 64); e == nil {
							value = n
						}
					}
				case "boolean":
					value = false
					if hasDefault {
						value, _ = strconv.ParseBool(defaultValue)
					}
				default:
					if hasDefault {
						value = strings.Trim(defaultValue, "'\"")
					}
				}
				if nullable && !hasDefault {
					value = nil
				}
				defaults[columnName] = value
				if strings.Contains(sqlType, "CHAR") || strings.Contains(sqlType, "TEXT") || columnName == "status" {
					search = append(search, columnName)
					fmt.Fprintf(&tsSearch, "  %s?: %s\n", columnName, strings.TrimSuffix(tsType, " | null"))
					if columnName != "status" {
						fmt.Fprintf(&searchFields, "%s,", strconv.Quote(columnName))
					}
				}
			}
		}
		if !hasID {
			response.Fail(c, 422, "当前生成器要求数据表包含id主键")
			return
		}
		module := config.GetString("dawn.module")
		if module == "" {
			response.Fail(c, 422, "请在config/dawn.go配置module为项目go.mod的模块名")
			return
		}
		source := fmt.Sprintf("package models\nimport \"github.com/dawnim/dawn-server/app/models\"\ntype %s struct{models.Base\n%s}\nfunc(%s)TableName()string{return %s}\n", name, fields.String(), name, strconv.Quote(table))
		scope, prepare := "", ""
		if hasOwner {
			department := ""
			if hasDepartment {
				department = "department_id"
			}
			scope = ".Scopes(auth.Actor(c).Scope(\"created_by\", " + strconv.Quote(department) + "))"
			prepare = "row.CreatedBy=int64(auth.Actor(c).User.ID)"
			if nullableFields["created_by"] {
				prepare = "createdBy:=int64(auth.Actor(c).User.ID);row.CreatedBy=&createdBy"
			}
			if hasDepartment {
				if nullableFields["department_id"] {
					prepare += ";departmentID:=int64(auth.Actor(c).User.DepartmentID);row.DepartmentID=&departmentID"
				} else {
					prepare += ";row.DepartmentID=int64(auth.Actor(c).User.DepartmentID)"
				}
			}
		}
		resourceScope := ""
		writableFields := selects.String()
		for _, field := range []string{"created_by", "department_id"} {
			writableFields = strings.ReplaceAll(writableFields, strconv.Quote(field)+",", "")
		}
		imports := ""
		if hasOwner {
			department := ""
			if hasDepartment {
				department = "department_id"
			}
			imports = ";\"gorm.io/gorm\""
			resourceScope = ",Scope:func(db *gorm.DB,actor *sdk.Actor)*gorm.DB{return db.Scopes(actor.Scope(\"created_by\"," + strconv.Quote(department) + "))},Prepare:func(row *models." + name + ",actor *sdk.Actor,editing bool){if editing{return};" + strings.ReplaceAll(prepare, "auth.Actor(c)", "actor") + "}"
		}
		controller := fmt.Sprintf("package controllers\nimport(sdk \"github.com/dawnim/dawn-server/app/admin\";crud \"github.com/dawnim/dawn-server/app/admin/controllers\";\"%s/app/models\";\"github.com/prismgo/framework/route\"%s)\nfunc Register%s(a *sdk.App,g *route.Registrar){crud.RegisterResource(a,g,crud.Resource[models.%s]{Path:%s,Permission:%s,Fields:[]string{%s},Required:[]string{%s},Search:[]string{%s},Importable:true%s})}\n", module, imports, name, name, strconv.Quote("/"+resource), strconv.Quote("app."+table), selects.String(), requiredFields.String(), searchFields.String(), resourceScope)
		if mode == "full" {
			controller = strings.NewReplacer("{{module}}", module, "{{name}}", name, "{{table}}", table, "{{fields}}", selects.String(), "{{scope}}", scope, "{{prepare}}", prepare, "{{search}}", searchFields.String(), "{{resource}}", resource, "{{required}}", requiredFields.String(), "{{writable}}", writableFields, "{{file_prepare}}", resourceScope).Replace(fullController)
		}
		modelSource, err := format.Source([]byte(source))
		if err != nil {
			response.Report(c, err)
			return
		}
		controllerSource, err := format.Source([]byte(controller))
		if err != nil {
			response.Report(c, err)
			return
		}
		files := map[string][]byte{"app/models/" + table + ".go": modelSource, "app/admin/controllers/" + table + ".go": controllerSource}
		definitionJSON, _ := json.MarshalIndent(definitions, "", "  ")
		searchJSON, _ := json.Marshal(search)
		defaultJSON, _ := json.MarshalIndent(defaults, "", "  ")
		replace := strings.NewReplacer("@@table@@", table, "@@directory@@", directory, "@@resource@@", resource, "@@name@@", name, "@@ts_fields@@", tsFields.String(), "@@ts_inputs@@", tsInputs.String(), "@@ts_search@@", tsSearch.String(), "@@columns@@", string(definitionJSON), "@@search@@", string(searchJSON), "@@defaults@@", string(defaultJSON))
		templates := map[string]string{"generator/api.ts.tpl": "admin/src/api/" + directory + "/" + table + ".ts", "generator/simple.vue.tpl": "admin/src/views/" + directory + "/" + table + "/index.vue"}
		if mode == "full" {
			delete(templates, "generator/simple.vue.tpl")
			templates["generator/index.vue.tpl"] = "admin/src/views/" + directory + "/" + table + "/index.vue"
			templates["generator/table-search.vue.tpl"] = "admin/src/views/" + directory + "/" + table + "/modules/table-search.vue"
			templates["generator/edit-dialog.vue.tpl"] = "admin/src/views/" + directory + "/" + table + "/modules/edit-dialog.vue"
		}
		for source, path := range templates {
			raw, err := generatorTemplates.ReadFile(source)
			if err != nil {
				response.Report(c, err)
				return
			}
			files[path] = []byte(replace.Replace(string(raw)))
		}
		for locale, labels := range translations {
			languageJSON, _ := json.MarshalIndent(map[string]any{"fields": map[string]any{table: labels}}, "", "  ")
			files["admin/src/locales/"+locale+"/"+table+".ts"] = []byte("export default " + string(languageJSON) + "\n")
		}

		if c.Query("migration") == "1" {
			source, err := generateMigration(a.DB, table, columns)
			if err != nil {
				response.Report(c, err)
				return
			}
			path := "database/migrations/" + time.Now().Format("20060102150405") + "_create_" + table + ".go"
			for name := range input.Files {
				if matched, _ := regexp.MatchString(`^database/migrations/[0-9]{14}_create_`+table+`\.go$`, name); matched {
					path = name
				}
			}
			files[path] = source
		}
		if c.Request.Method == http.MethodPost {
			if len(input.Files) != len(files) {
				response.Fail(c, 422, "生成文件列表不匹配")
				return
			}
			for name, data := range input.Files {
				if _, ok := files[name]; !ok {
					response.Fail(c, 422, "生成文件列表不匹配")
					return
				}
				files[name] = []byte(data)
			}
		}
		if writing {
			root, err := os.OpenRoot(support.BasePath())
			if err != nil {
				response.Report(c, err)
				return
			}
			defer root.Close()
			created := []string{}
			for name, data := range files {
				if err = root.MkdirAll(filepath.Dir(name), 0750); err != nil {
					break
				}
				var file *os.File
				file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
				if err != nil {
					break
				}
				created = append(created, name)
				_, err = file.Write(data)
				closeErr := file.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					break
				}
			}
			if err != nil {
				for _, name := range created {
					_ = root.Remove(name)
				}
				response.Report(c, err, 409)
				return
			}
			response.Respond(c, gin.H{"files": created})
			return
		}
		if c.Request.Method == http.MethodGet {
			result := map[string]string{}
			for name, data := range files {
				result[name] = string(data)
			}
			response.Respond(c, result)
			return
		}
		var buffer bytes.Buffer
		archive := zip.NewWriter(&buffer)
		for name, data := range files {
			writer, err := archive.Create(name)
			if err != nil {
				response.Report(c, err)
				return
			}
			if _, err := writer.Write(data); err != nil {
				response.Report(c, err)
				return
			}
		}
		if err := archive.Close(); err != nil {
			response.Report(c, err)
			return
		}
		c.Header("Content-Disposition", "attachment; filename=crud.zip")
		c.Data(200, "application/zip", buffer.Bytes())
	}
	g.Get("/code/previews", middleware.Require(a.App, "dawn.generator.list"), generate)
	g.Post("/code/archives", middleware.Require(a.App, "dawn.generator.list"), generate)
	g.Post("/code/generations", middleware.Require(a.App, "dawn.generator.create"), generate)
}
func goName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
		if slices.Contains([]string{"id", "ip", "http", "https", "url", "json", "sql", "utc", "pid", "uid", "smtp", "mime", "md5", "sha256"}, p) {
			parts[i] = strings.ToUpper(p)
		}
	}
	return strings.Join(parts, "")
}

// 完整模式直接生成可修改的路由和CRUD处理，查询与写入不经过通用Resource。
const fullController = `package controllers
import (
 "fmt"
 "{{module}}/app/models"
 sdk "github.com/dawnim/dawn-server/app/admin"
 crud "github.com/dawnim/dawn-server/app/admin/controllers"
 auth "github.com/dawnim/dawn-server/app/admin/middleware"
 "github.com/dawnim/dawn-server/app/admin/response"
 "github.com/gin-gonic/gin"
 "github.com/prismgo/framework/route"
 "gorm.io/gorm"
 "slices"
)
func Register{{name}}(a *sdk.App,g *route.Registrar) {
 fields:=[]string{ {{fields}} }
 writable:=[]string{ {{writable}} }
 required:=[]string{ {{required}} }
 query:=func(c *gin.Context,db *gorm.DB)*gorm.DB{return db.WithContext(c.Request.Context()).Model(&models.{{name}}{}){{scope}}}
 filtered:=func(c *gin.Context)*gorm.DB{
  db:=query(c,a.DB)
  for _,field:=range []string{ {{search}} }{if value:=c.Query(field);value!=""{db=db.Where(field+" LIKE ?","%"+value+"%")}}
  if slices.Contains(fields,"status") && c.Query("status")!=""{db=db.Where("status = ?",c.Query("status"))}
  return db
 }
 files:=crud.RegisterResourceFiles(a,g,crud.Resource[models.{{name}}]{Path:"/{{resource}}",Permission:"app.{{table}}",Fields:fields,Required:required,Importable:true{{file_prepare}}},filtered)
 g.Get("/{{resource}}",auth.Require(a,"app.{{table}}.list"),func(c *gin.Context){
  if c.NegotiateFormat("application/json","text/csv")=="text/csv"{if !auth.Actor(c).Can("app.{{table}}.export"){response.Fail(c,403,"没有操作权限");return};files(c);return}
  db:=filtered(c)
  var total int64
  if err:=db.Session(&gorm.Session{}).Count(&total).Error;err!=nil{response.Report(c,err);return}
  page,size:=response.Page(c)
  rows:=[]models.{{name}}{}
  if err:=db.Order(response.Order(c,append(slices.Clone(fields),"id","created_at","updated_at")...)).Order("id DESC").Offset((page-1)*size).Limit(size).Find(&rows).Error;err!=nil{response.Report(c,err);return}
  response.Paginated(c,rows,total,page,size)
 }).Name("app.{{table}}.index")
 g.Get("/{{resource}}/{id}",auth.Require(a,"app.{{table}}.list"),func(c *gin.Context){
  id:=response.ID(c);if c.IsAborted(){return}
  var row models.{{name}}
  if err:=query(c,a.DB).First(&row,id).Error;err!=nil{response.Report(c,err);return}
  response.Respond(c,row)
 }).Name("app.{{table}}.show")
 g.Post("/{{resource}}",auth.Require(a,"app.{{table}}.create"),func(c *gin.Context){
  var row models.{{name}}
  submitted:=crud.BindResourceInput(a.DB,c,&row,writable);if submitted==nil{return}
  for _,field:=range required{if !slices.Contains(submitted,field){response.Fail(c,422,"缺少必填字段: %s",field);return}}
  {{prepare}}
  if err:=crud.CreateResource(a.DB.WithContext(c.Request.Context()),&row,fields,submitted);err!=nil{response.Report(c,err);return}
  response.Created(c,row,fmt.Sprintf("%s/%d",c.Request.URL.Path,row.ID))
 }).Name("app.{{table}}.store")
 update:=func(c *gin.Context){
  id:=response.ID(c);if c.IsAborted(){return}
  var row models.{{name}}
  if err:=query(c,a.DB).First(&row,id).Error;err!=nil{response.Report(c,err);return}
  selected:=crud.BindResourceInput(a.DB,c,&row,writable);if selected==nil{return}
  if c.Request.Method=="PUT"{for _,field:=range required{if !slices.Contains(selected,field){response.Fail(c,422,"缺少必填字段: %s",field);return}};selected=writable}
  if err:=query(c,a.DB).Where("id = ?",id).Select(selected).Updates(&row).Error;err!=nil{response.Report(c,err);return}
  response.Respond(c,row)
 }
 g.Match([]string{"PUT","PATCH"},"/{{resource}}/{id}",auth.Require(a,"app.{{table}}.update"),update).Name("app.{{table}}.update")
 remove:=func(c *gin.Context){
  ids:=response.IDs(c);if c.IsAborted(){return}
  if err:=a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB)error{
   db:=query(c,tx).Where("id IN ?",ids)
   var total int64
   if err:=db.Session(&gorm.Session{}).Count(&total).Error;err!=nil{return err}
   if total!=int64(len(ids)){return gorm.ErrRecordNotFound}
   return db.Delete(&models.{{name}}{}).Error
  });err!=nil{response.Report(c,err);return}
  c.Status(204)
 }
 g.Delete("/{{resource}}",auth.Require(a,"app.{{table}}.delete"),remove).Name("app.{{table}}.destroy-many")
 g.Delete("/{{resource}}/{id}",auth.Require(a,"app.{{table}}.delete"),remove).Name("app.{{table}}.destroy")
}
`

var frontendDirectory = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
