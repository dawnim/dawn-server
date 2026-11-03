package controllers

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

func (a *Controller) DatabaseRoutes(g *route.Registrar) {
	g.Get("/tables", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		rows := []map[string]any{}
		var err error
		switch a.DB.Dialector.Name() {
		case "mysql":
			err = a.DB.Raw(`SELECT TABLE_NAME AS name, TABLE_COMMENT AS comment, ENGINE AS engine,
				TABLE_ROWS AS ` + "`rows`" + `, DATA_FREE AS data_free, DATA_LENGTH AS data_length,
				INDEX_LENGTH AS index_length, TABLE_COLLATION AS collation,
				CAST(UNIX_TIMESTAMP(CREATE_TIME) * 1000 AS SIGNED) AS created_at,
				CAST(UNIX_TIMESTAMP(UPDATE_TIME) * 1000 AS SIGNED) AS updated_at
				FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`).Scan(&rows).Error
		case "postgres":
			err = a.DB.Raw(`SELECT c.relname AS name, COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment,
				am.amname AS engine, CASE WHEN c.reltuples >= 0 THEN c.reltuples::bigint END AS rows,
				pg_table_size(c.oid) AS data_length, pg_indexes_size(c.oid) AS index_length,
				NULL AS data_free, NULL AS collation, NULL AS created_at, NULL AS updated_at
				FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				LEFT JOIN pg_am am ON am.oid = c.relam
				WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') ORDER BY c.relname`).Scan(&rows).Error
		case "sqlite":
			var tables []string
			tables, err = a.DB.Migrator().GetTables()
			if err != nil {
				response.Report(c, err)
				return
			}
			for _, table := range tables {
				var count int64
				if err := a.DB.Table(table).Count(&count).Error; err != nil {
					response.Report(c, err)
					return
				}
				rows = append(rows, gin.H{"name": table, "comment": "", "engine": "SQLite", "rows": count,
					"data_free": nil, "data_length": nil, "index_length": nil, "collation": nil, "created_at": nil, "updated_at": nil})
			}
		default:
			response.Fail(c, 422, "该驱动未提供表维护")
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		raw, _ := json.Marshal(rows)
		_ = json.Unmarshal(raw, &rows)
		rows = slices.DeleteFunc(rows, func(row map[string]any) bool { return !strings.Contains(row["name"].(string), c.Query("name")) })
		field := c.DefaultQuery("sort_by", "name")
		if !slices.Contains([]string{"name", "created_at", "updated_at"}, field) {
			field = "name"
		}
		sort.SliceStable(rows, func(i, j int) bool {
			order := 0
			if field == "name" {
				order = strings.Compare(rows[i][field].(string), rows[j][field].(string))
			} else {
				left, _ := rows[i][field].(float64)
				right, _ := rows[j][field].(float64)
				order = cmp.Compare(left, right)
			}
			if c.Query("sort_order") == "desc" {
				return order > 0
			}
			return order < 0
		})
		page, size := response.Page(c)
		total := len(rows)
		start := min((page-1)*size, total)
		end := min(start+size, total)
		response.Paginated(c, rows[start:end], int64(total), page, size)
	})

	g.Get("/table-names", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, tables)
	})
	g.Get("/tables/{table}/columns", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		table := c.Param("table")
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		if !slices.Contains(tables, table) {
			response.Fail(c, 404, "数据表不存在")
			return
		}
		indexes, err := tableIndexes(a.DB, table)
		if err != nil {
			response.Report(c, err)
			return
		}
		keys := map[string]string{}
		for _, index := range indexes {
			fields := index.Columns()
			if len(fields) == 0 {
				continue
			}
			if unique, _ := index.Unique(); unique && len(fields) == 1 {
				keys[fields[0]] = "UNI"
			} else if keys[fields[0]] == "" {
				keys[fields[0]] = "MUL"
			}
		}
		data := []map[string]any{}
		if a.DB.Dialector.Name() == "sqlite" {
			// 使用原生元数据，避免驱动解析建表SQL时丢失默认值和非空约束。
			err = a.DB.Raw(`SELECT name, type, '' AS comment, dflt_value AS "default",
				"notnull" = 0 AS nullable, pk > 0 AS "primary" FROM pragma_table_info(?) ORDER BY cid`, table).Scan(&data).Error
			if err != nil {
				response.Report(c, err)
				return
			}
			for _, column := range data {
				column["nullable"] = column["nullable"] == int64(1)
				column["primary"] = column["primary"] == int64(1)
			}
		} else {
			columns, err := a.DB.Migrator().ColumnTypes(table)
			if err != nil {
				response.Report(c, err)
				return
			}
			for _, column := range columns {
				nullable, _ := column.Nullable()
				primary, _ := column.PrimaryKey()
				comment, _ := column.Comment()
				fullType, _ := column.ColumnType()
				var defaultValue any
				if value, ok := column.DefaultValue(); ok {
					defaultValue = value
				}
				data = append(data, gin.H{"name": column.Name(), "type": cmp.Or(fullType, column.DatabaseTypeName()), "nullable": nullable,
					"primary": primary, "comment": comment, "default": defaultValue})
			}
		}
		for _, column := range data {
			key := keys[column["name"].(string)]
			if column["primary"] == true {
				key = "PRI"
			}
			column["key"] = key
		}
		response.Respond(c, data)
	})
	g.Post("/table-maintenance-runs", middleware.Require(a.App, "dawn.database.update"), func(c *gin.Context) {
		if !middleware.Actor(c).User.Super {
			response.Fail(c, 403, "数据表维护仅超级管理员可执行")
			return
		}
		var input struct {
			Table  string `json:"table" binding:"required"`
			Action string `json:"action" binding:"required,oneof=analyze optimize"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "维护参数错误")
			return
		}
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		if !slices.Contains(tables, input.Table) {
			response.Fail(c, 404, "数据表不存在")
			return
		}
		statement := &gorm.Statement{DB: a.DB}
		table := statement.Quote(input.Table)
		sql := ""
		switch a.DB.Dialector.Name() {
		case "mysql":
			if input.Action == "analyze" {
				sql = "ANALYZE TABLE " + table
			} else {
				sql = "OPTIMIZE TABLE " + table
			}
		case "postgres":
			if input.Action == "analyze" {
				sql = "ANALYZE " + table
			} else {
				sql = "VACUUM (ANALYZE) " + table
			}
		case "sqlite":
			if input.Action == "analyze" {
				sql = "ANALYZE " + table
			} else {
				sql = "VACUUM"
			}
		default:
			response.Fail(c, 422, "该驱动未提供表维护")
			return
		}
		if err := a.DB.Exec(sql).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.Get("/recycle-bin", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		if !middleware.Actor(c).User.Super {
			response.Fail(c, 403, "回收站仅超级管理员可操作")
			return
		}
		model, ok := recyclable[c.Query("resource")]
		if !ok {
			response.Fail(c, 422, "资源未注册")
			return
		}
		rows := []map[string]any{}
		db := a.DB.Unscoped().Model(model).Where("deleted_at > 0")
		var total int64
		if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		if err := db.Order(response.Order(c, "id", "deleted_at")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		for _, row := range rows {
			delete(row, "password")
		}
		response.Paginated(c, rows, total, page, size)
	})
	g.Post("/recycle-bin/restorations", middleware.Require(a.App, "dawn.database.update"), func(c *gin.Context) {
		if !middleware.Actor(c).User.Super {
			response.Fail(c, 403, "回收站仅超级管理员可操作")
			return
		}
		var input struct {
			Resource string `json:"resource" binding:"required"`
			IDs      []uint `json:"ids" binding:"required,min=1,dive,gt=0"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "参数格式错误")
			return
		}
		model, ok := recyclable[input.Resource]
		if !ok {
			response.Fail(c, 422, "资源未注册")
			return
		}
		slices.Sort(input.IDs)
		input.IDs = slices.Compact(input.IDs)
		if err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			db := tx.Unscoped().Model(model).Where("id IN ? AND deleted_at > 0", input.IDs)
			var total int64
			if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			if total != int64(len(input.IDs)) {
				return gorm.ErrRecordNotFound
			}
			return db.Update("deleted_at", 0).Error
		}); err != nil {
			response.Report(c, err)
			return
		}
		if slices.Contains([]string{"users", "roles", "posts", "menus"}, input.Resource) {
			if err := a.ClearUserCaches(c.Request.Context()); err != nil {
				response.Report(c, err)
				return
			}
		}
		c.Status(204)
	})

}

var recyclable = map[string]any{"users": &models.User{}, "roles": &models.Role{}, "departments": &models.Department{}, "posts": &models.Post{}, "attachments": &models.Attachment{}, "categories": &models.Category{}, "menus": &models.Menu{}}
var identifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
