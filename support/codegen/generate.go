// Package codegen generates the same CRUD source for the admin UI and CLI.
package codegen

import (
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/support/i18n"
	"gorm.io/gorm"
)

//go:embed templates/*.tpl
var generatorTemplates embed.FS

// Options selects the source table and output layout, relative to the output root.
type Options struct {
	Table, Resource, Directory, Mode, Module string
	Backend, Frontend                        string
	Migration                                bool
}

// Tables lists business tables without filtering by a table-name prefix.
func Tables(db *gorm.DB) ([]string, error) {
	tables, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	builtins := migrations.Definition().Tables
	tables = slices.DeleteFunc(tables, func(name string) bool {
		_, builtin := builtins[name]
		return builtin || name == "migrations" || name == "sqlite_sequence"
	})
	slices.Sort(tables)
	return tables, nil
}

// Generate reads metadata and returns source files without writing them.
func Generate(db *gorm.DB, o Options) (map[string][]byte, error) {
	table := o.Table
	if !identifier.MatchString(table) {
		return nil, i18n.Errorf("表名格式错误")
	}
	if _, builtin := migrations.Definition().Tables[table]; builtin || table == "migrations" || table == "sqlite_sequence" {
		return nil, i18n.Errorf("不能生成扩展内置表的业务接口")
	}
	if !db.Migrator().HasTable(table) {
		return nil, gorm.ErrRecordNotFound
	}
	resource := o.Resource
	if resource == "" {
		resource = strings.ReplaceAll(table, "_", "-")
	}
	if !resourceName.MatchString(resource) {
		return nil, i18n.Errorf("资源名格式错误")
	}
	if slices.Contains([]string{"users", "members", "roles", "departments", "posts", "menus", "navigation", "settings", "sessions", "logs", "mail-tests", "attachments", "attachment-checks", "uploads", "cache", "server", "tables", "table-names", "table-maintenance-runs", "recycle-bin", "code", "horizon", "auth", "links", "realtime", "chat", "support-apps", "support-sessions", "knowledge", "knowledge-entries", "knowledge-sources", "ai-runs", "skills"}, resource) {
		return nil, i18n.Errorf("资源名与内置接口冲突")
	}
	directory := o.Directory
	if directory == "" {
		directory = "business"
	}
	if !frontendDirectory.MatchString(directory) {
		return nil, i18n.Errorf("前端目录格式错误")
	}
	mode := o.Mode
	if mode == "" {
		mode = "simple"
	}
	if mode != "simple" && mode != "full" {
		return nil, i18n.Errorf("参数格式错误")
	}
	if o.Backend == "" {
		o.Backend = "app"
	}
	if o.Frontend == "" {
		o.Frontend = "admin/src"
	}
	o.Backend, o.Frontend = path.Clean(o.Backend), path.Clean(o.Frontend)
	if !sourceDirectory.MatchString(o.Backend) || !sourceDirectory.MatchString(o.Frontend) {
		return nil, i18n.Errorf("生成目录必须为输出目录内的相对路径")
	}
	columns, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return nil, err
	}
	name := goName(table)
	indexTags, err := columnIndexTags(db, table)
	if err != nil {
		return nil, err
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
			return nil, i18n.Errorf("字段名不适合生成Go代码")
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
		label, _, _ = strings.Cut(label, ":")
		label, _, _ = strings.Cut(label, "：")
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
		if indices := indexTags[columnName]; len(indices) > 0 {
			tags += ";" + strings.Join(indices, ";")
		}
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
		definition := map[string]any{"prop": columnName, "label": "fields." + table + "." + columnName, "type": inputType, "readonly": readonly, "nullable": nullable}
		defaultValue, hasDefault := column.DefaultValue()
		required := !nullable && !hasDefault && !readonly
		definition["required"] = required
		validation := []string{}
		if strings.TrimPrefix(kind, "*") == "string" {
			if required {
				validation = append(validation, "required")
			} else {
				validation = append(validation, "omitempty")
			}
			if length, ok := column.Length(); ok && length > 0 && strings.Contains(sqlType, "CHAR") {
				validation = append(validation, fmt.Sprintf("max=%d", length))
			}
		}
		binding := ""
		if len(validation) > 0 {
			binding = " binding:" + strconv.Quote(strings.Join(validation, ","))
		}
		fmt.Fprintf(&fields, "\t%s %s `json:%s gorm:%s%s`\n", goName(columnName), kind, strconv.Quote(columnName), strconv.Quote(tags), binding)
		if !readonly {
			optional := ""
			if nullable || hasDefault {
				optional = "?"
			} else {
				fmt.Fprintf(&requiredFields, "%q,", columnName)
			}
			fmt.Fprintf(&tsInputs, "  %s%s: %s\n", columnName, optional, tsType)
		}
		if length, ok := column.Length(); ok && length > 0 && strings.Contains(sqlType, "CHAR") {
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
		return nil, i18n.Errorf("当前生成器要求数据表包含id主键")
	}
	module := o.Module
	if module == "" {
		return nil, i18n.Errorf("请在config/dawn.go配置module为项目go.mod的模块名")
	}
	source := fmt.Sprintf("package models\nimport \"github.com/dawnim/dawn-server/app/models\"\ntype %s struct{models.Base\n%s}\nfunc(%s)TableName()string{return %s}\n", name, fields.String(), name, strconv.Quote(table))
	scope, prepare, resourceScope, imports := "", "", "", ""
	if hasOwner || hasDepartment {
		owner, department := "", ""
		if hasOwner {
			owner = "created_by"
		}
		if hasDepartment {
			department = "department_id"
		}
		scope = ".Scopes(auth.Actor(c).Scope(" + strconv.Quote(owner) + "," + strconv.Quote(department) + "))"
		for _, field := range []struct {
			column, goField, value string
			exists                 bool
		}{
			{"created_by", "CreatedBy", "User.ID", hasOwner},
			{"department_id", "DepartmentID", "User.DepartmentID", hasDepartment},
		} {
			if !field.exists {
				continue
			}
			value := "int64(auth.Actor(c)." + field.value + ")"
			if nullableFields[field.column] {
				prepare += field.column + ":=" + value + ";row." + field.goField + "=&" + field.column + ";"
			} else {
				prepare += "row." + field.goField + "=" + value + ";"
			}
		}
		imports = ";\"gorm.io/gorm\""
		resourceScope = ",Scope:func(db *gorm.DB,actor *sdk.Actor)*gorm.DB{return db.Scopes(actor.Scope(" + strconv.Quote(owner) + "," + strconv.Quote(department) + "))},Prepare:func(row *models." + name + ",actor *sdk.Actor,editing bool){if editing{return};" + strings.ReplaceAll(prepare, "auth.Actor(c)", "actor") + "}"
	}
	writableFields := selects.String()
	for _, field := range []string{"created_by", "department_id"} {
		writableFields = strings.ReplaceAll(writableFields, strconv.Quote(field)+",", "")
	}
	controller := fmt.Sprintf("package controllers\nimport(sdk \"github.com/dawnim/dawn-server/app/admin\";crud \"github.com/dawnim/dawn-server/app/admin/controllers\";\"%s/models\";\"github.com/prismgo/framework/route\"%s)\nfunc Register%s(a *sdk.App,g *route.Registrar){crud.RegisterResource(a,g,crud.Resource[models.%s]{Path:%s,Permission:%s,Fields:[]string{%s},Required:[]string{%s},Search:[]string{%s},Importable:true%s})}\n", module+"/"+o.Backend, imports, name, name, strconv.Quote("/"+resource), strconv.Quote("app."+table), selects.String(), requiredFields.String(), searchFields.String(), resourceScope)
	if mode == "full" {
		controller = strings.NewReplacer("{{module}}", module, "{{backend}}", o.Backend, "{{name}}", name, "{{table}}", table, "{{fields}}", selects.String(), "{{scope}}", scope, "{{prepare}}", prepare, "{{search}}", searchFields.String(), "{{resource}}", resource, "{{required}}", requiredFields.String(), "{{writable}}", writableFields, "{{file_prepare}}", resourceScope).Replace(fullController)
	}
	modelSource, err := format.Source([]byte(source))
	if err != nil {
		return nil, err
	}
	controllerSource, err := format.Source([]byte(controller))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{o.Backend + "/models/" + table + ".go": modelSource, o.Backend + "/admin/controllers/" + table + ".go": controllerSource}
	definitionJSON, _ := json.MarshalIndent(definitions, "", "  ")
	searchJSON, _ := json.Marshal(search)
	defaultJSON, _ := json.MarshalIndent(defaults, "", "  ")
	replace := strings.NewReplacer("@@table@@", table, "@@directory@@", directory, "@@resource@@", resource, "@@name@@", name, "@@ts_fields@@", tsFields.String(), "@@ts_inputs@@", tsInputs.String(), "@@ts_search@@", tsSearch.String(), "@@columns@@", string(definitionJSON), "@@search@@", string(searchJSON), "@@defaults@@", string(defaultJSON))
	templates := map[string]string{"templates/api.ts.tpl": o.Frontend + "/api/" + directory + "/" + table + ".ts", "templates/simple.vue.tpl": o.Frontend + "/views/" + directory + "/" + table + "/index.vue"}
	if mode == "full" {
		delete(templates, "templates/simple.vue.tpl")
		templates["templates/index.vue.tpl"] = o.Frontend + "/views/" + directory + "/" + table + "/index.vue"
		templates["templates/table-search.vue.tpl"] = o.Frontend + "/views/" + directory + "/" + table + "/modules/table-search.vue"
		templates["templates/edit-dialog.vue.tpl"] = o.Frontend + "/views/" + directory + "/" + table + "/modules/edit-dialog.vue"
	}
	for source, path := range templates {
		raw, err := generatorTemplates.ReadFile(source)
		if err != nil {
			return nil, err
		}
		files[path] = []byte(replace.Replace(string(raw)))
	}
	for locale, labels := range translations {
		languageJSON, _ := json.MarshalIndent(map[string]any{"fields": map[string]any{table: labels}}, "", "  ")
		files[o.Frontend+"/locales/"+locale+"/"+table+".ts"] = []byte("export default " + string(languageJSON) + "\n")
	}

	if o.Migration {
		source, err := generateMigration(db, table, columns)
		if err != nil {
			return nil, err
		}
		files["database/migrations/"+time.Now().Format("20060102150405")+"_create_"+table+".go"] = source
	}
	return files, nil
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
 "{{module}}/{{backend}}/models"
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
 handlers:=crud.ResourceHandlers{}
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
 handlers.Index=func(c *gin.Context){
  if c.NegotiateFormat("application/json","text/csv")=="text/csv"{if !auth.Actor(c).Can("app.{{table}}.export"){response.Fail(c,403,"没有操作权限");return};files(c);return}
  db:=filtered(c)
  var total int64
  if err:=db.Session(&gorm.Session{}).Count(&total).Error;err!=nil{response.Report(c,err);return}
  page,size:=response.Page(c)
  rows:=[]models.{{name}}{}
  if err:=db.Order(response.Order(c,append(slices.Clone(fields),"id","created_at","updated_at")...)).Order("id DESC").Offset((page-1)*size).Limit(size).Find(&rows).Error;err!=nil{response.Report(c,err);return}
  response.Paginated(c,rows,total,page,size)
 }
 handlers.Show=func(c *gin.Context){
  id:=response.ID(c);if c.IsAborted(){return}
  var row models.{{name}}
  if err:=query(c,a.DB).First(&row,id).Error;err!=nil{response.Report(c,err);return}
  response.Respond(c,row)
 }
 handlers.Store=func(c *gin.Context){
  var row models.{{name}}
  submitted:=crud.BindResourceInput(a.DB,c,&row,writable);if submitted==nil{return}
  for _,field:=range required{if !slices.Contains(submitted,field){response.Fail(c,422,"缺少必填字段: %s",field);return}}
  {{prepare}}
  if err:=crud.CreateResource(a.DB.WithContext(c.Request.Context()),&row,fields,submitted);err!=nil{response.Report(c,err);return}
  response.Created(c,row,fmt.Sprintf("%s/%d",c.Request.URL.Path,row.ID))
 }
 handlers.Update=func(c *gin.Context){
  id:=response.ID(c);if c.IsAborted(){return}
  var row models.{{name}}
  if err:=query(c,a.DB).First(&row,id).Error;err!=nil{response.Report(c,err);return}
  selected:=crud.BindResourceInput(a.DB,c,&row,writable);if selected==nil{return}
  if c.Request.Method=="PUT"{for _,field:=range required{if !slices.Contains(selected,field){response.Fail(c,422,"缺少必填字段: %s",field);return}};selected=writable}
  if err:=query(c,a.DB).Where("id = ?",id).Select(selected).Updates(&row).Error;err!=nil{response.Report(c,err);return}
  response.Respond(c,row)
 }
 handlers.Destroy=func(c *gin.Context){
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
 crud.RegisterResourceRoutes(a,g,"/{{resource}}","app.{{table}}",handlers)
}
`

var frontendDirectory = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var sourceDirectory = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*(?:/[a-zA-Z_][a-zA-Z0-9_-]*)*$`)
