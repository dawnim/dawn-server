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
	"github.com/prismgo/framework/support"
)

//go:embed generator/*.tpl
var generatorTemplates embed.FS

var adminTables = []string{"admin_users", "admin_roles", "admin_user_roles", "admin_user_posts", "admin_role_permissions", "admin_departments", "admin_posts", "admin_sessions", "admin_settings", "admin_menus", "admin_files", "admin_attachments", "admin_categories", "admin_logs", "admin_migrations", "migrations", "sqlite_sequence"}

func (a *Controller) GeneratorRoutes(g *gin.RouterGroup) {
	g.GET("/development/tables", middleware.Require(a.App, "dawn.generator.list"), func(c *gin.Context) {
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, slices.DeleteFunc(tables, func(table string) bool { return slices.Contains(adminTables, table) }))
	})

	generate := func(c *gin.Context) {
		download := strings.HasSuffix(c.FullPath(), "/export")
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
		name := goName(table)
		mode := c.DefaultQuery("mode", "simple")
		if mode != "simple" && mode != "full" {
			response.Fail(c, 422, "参数格式错误")
			return
		}
		var fields, selects, searchFields strings.Builder
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
				label = english
			}
			if columnName == "created_at" {
				label, english = "创建时间", "Created at"
			}
			if columnName == "updated_at" {
				label, english = "更新时间", "Updated at"
			}
			translations["zh"][columnName], translations["en"][columnName] = label, english
			if columnName == "created_at" || columnName == "updated_at" {
				definitions = append(definitions, map[string]any{"prop": columnName, "label": columnName, "type": "date", "readonly": true})
				continue
			}
			kind := columnGoType(column)
			sqlType := strings.ToUpper(column.DatabaseTypeName())
			nullable, _ := column.Nullable()
			nullableFields[columnName] = nullable
			fmt.Fprintf(&fields, "\t%s %s `json:%s gorm:%s`\n", goName(columnName), kind, strconv.Quote(columnName), strconv.Quote("column:"+columnName))
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
			definition := map[string]any{"prop": columnName, "label": columnName, "type": inputType, "readonly": readonly}
			defaultValue, hasDefault := column.DefaultValue()
			definition["required"] = !nullable && !hasDefault && !readonly
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
		imports := ""
		if hasOwner {
			department := ""
			if hasDepartment {
				department = "department_id"
			}
			imports = ";\"gorm.io/gorm\""
			resourceScope = ",Scope:func(db *gorm.DB,actor *sdk.Actor)*gorm.DB{return db.Scopes(actor.Scope(\"created_by\"," + strconv.Quote(department) + "))},Prepare:func(row *models." + name + ",actor *sdk.Actor,editing bool){if editing{return};" + strings.ReplaceAll(prepare, "auth.Actor(c)", "actor") + "}"
		}
		controller := fmt.Sprintf("package controllers\nimport(sdk \"github.com/dawnim/dawn-server/app/admin\";crud \"github.com/dawnim/dawn-server/app/admin/controllers\";\"%s/app/models\";\"github.com/gin-gonic/gin\"%s)\nfunc Register%s(a *sdk.App,g *gin.RouterGroup){crud.RegisterResource(a,g,crud.Resource[models.%s]{Path:%s,Permission:%s,Fields:[]string{%s},Search:[]string{%s}%s})}\n", module, imports, name, name, strconv.Quote("/business/"+table), strconv.Quote("app."+table), selects.String(), searchFields.String(), resourceScope)
		if mode == "full" {
			controller = strings.NewReplacer("{{module}}", module, "{{name}}", name, "{{table}}", table, "{{fields}}", selects.String(), "{{scope}}", scope, "{{prepare}}", prepare, "{{search}}", searchFields.String()).Replace(fullController)
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
		replace := strings.NewReplacer("@@table@@", table, "@@name@@", name, "@@columns@@", string(definitionJSON), "@@search@@", string(searchJSON), "@@defaults@@", string(defaultJSON))
		templates := map[string]string{"generator/readme." + c.GetString("admin.locale") + ".md.tpl": "GENERATED_" + table + ".md", "generator/api.ts.tpl": "admin/src/api/business/" + table + ".ts", "generator/simple.vue.tpl": "admin/src/views/business/" + table + "/index.vue"}
		if mode == "full" {
			delete(templates, "generator/simple.vue.tpl")
			templates["generator/index.vue.tpl"] = "admin/src/views/business/" + table + "/index.vue"
			templates["generator/table-search.vue.tpl"] = "admin/src/views/business/" + table + "/modules/table-search.vue"
			templates["generator/edit-dialog.vue.tpl"] = "admin/src/views/business/" + table + "/modules/edit-dialog.vue"
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
			languageJSON, _ := json.MarshalIndent(labels, "", "  ")
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
	g.GET("/development/generate", middleware.Require(a.App, "dawn.generator.list"), generate)
	g.POST("/development/export", middleware.Require(a.App, "dawn.generator.list"), generate)
	g.POST("/development/generate", middleware.Require(a.App, "dawn.generator.create"), generate)
}
func goName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
		if p == "id" {
			parts[i] = "ID"
		}
	}
	return strings.Join(parts, "")
}

// 完整模式直接生成可修改的路由和CRUD处理，查询与写入不经过通用Resource。
const fullController = `package controllers
import (
 "{{module}}/app/models"
 sdk "github.com/dawnim/dawn-server/app/admin"
 auth "github.com/dawnim/dawn-server/app/admin/middleware"
 "github.com/dawnim/dawn-server/app/admin/response"
 "github.com/gin-gonic/gin"
)
func Register{{name}}(a *sdk.App,g *gin.RouterGroup) {
 fields:=[]string{ {{fields}} }
 g.GET("/business/{{table}}",auth.Require(a,"app.{{table}}.list"),func(c *gin.Context){
   db:=a.DB.WithContext(c.Request.Context()).Model(&models.{{name}}{}){{scope}}
   for _,field:=range []string{ {{search}} }{if value:=c.Query(field);value!=""{db=db.Where(field+" LIKE ?","%"+value+"%")}}
   for _,field:=range fields {if field=="status" && c.Query(field)!="" {db=db.Where("status = ?",c.Query(field))}}
   // 在这里添加业务筛选与数据范围。
   var total int64
   if err:=db.Count(&total).Error;err!=nil { response.Report(c,err);return }
   page,size:=response.Page(c)
   rows:=[]models.{{name}}{}
   if err:=db.Order(response.Order(c,append(fields,"id","created_at","updated_at")...)).Offset((page-1)*size).Limit(size).Find(&rows).Error;err!=nil { response.Report(c,err);return }
   response.Respond(c,gin.H{"data":rows,"total":total,"current_page":page,"per_page":size})
 })
 g.GET("/business/{{table}}/:id",auth.Require(a,"app.{{table}}.list"),func(c *gin.Context){
   id:=response.ID(c);if c.IsAborted(){return}
   var row models.{{name}}
   if err:=a.DB{{scope}}.First(&row,id).Error;err!=nil {response.Report(c,err);return}
   response.Respond(c,row)
 })
 g.POST("/business/{{table}}",auth.Require(a,"app.{{table}}.create"),func(c *gin.Context){
   var row models.{{name}}
   if err:=c.ShouldBindJSON(&row);err!=nil {response.Fail(c,422,"参数格式错误");return}
   row.ID=0
   {{prepare}}
   if err:=a.DB.Select(fields).Create(&row).Error;err!=nil {response.Report(c,err);return}
   response.Respond(c,row)
 })
 g.PUT("/business/{{table}}/:id",auth.Require(a,"app.{{table}}.update"),func(c *gin.Context){
   id:=response.ID(c);if c.IsAborted(){return}
   var row models.{{name}}
   if err:=a.DB{{scope}}.First(&row,id).Error;err!=nil {response.Report(c,err);return}
   var input models.{{name}}
   if err:=c.ShouldBindJSON(&input);err!=nil {response.Fail(c,422,"参数格式错误");return}
   if err:=a.DB{{scope}}.Model(&row).Select(fields).Omit("created_by","department_id").Updates(&input).Error;err!=nil {response.Report(c,err);return}
   response.Respond(c,nil)
 })
 g.DELETE("/business/{{table}}/:id",auth.Require(a,"app.{{table}}.delete"),func(c *gin.Context){
   id:=response.ID(c);if c.IsAborted(){return}
   if err:=a.DB{{scope}}.Delete(&models.{{name}}{},id).Error;err!=nil {response.Report(c,err);return}
   response.Respond(c,nil)
 })
}
`
