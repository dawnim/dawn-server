package controllers

import (
	"regexp"
	"slices"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *Controller) DatabaseRoutes(g *gin.RouterGroup) {
	g.GET("/database/table-info", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		rows := []gin.H{}
		for _, table := range tables {
			var count int64
			if err := a.DB.Table(table).Count(&count).Error; err != nil {
				response.Report(c, err)
				return
			}
			comment, engine := "", a.DB.Dialector.Name()
			switch engine {
			case "mysql":
				err = a.DB.Raw("SELECT table_comment FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&comment).Error
			case "postgres":
				err = a.DB.Raw("SELECT obj_description(to_regclass(?), 'pg_class')", table).Scan(&comment).Error
			}
			if err != nil {
				response.Report(c, err)
				return
			}
			rows = append(rows, gin.H{"name": table, "comment": comment, "engine": engine, "rows": count})
		}
		response.Respond(c, rows)
	})

	g.GET("/database/tables", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, tables)
	})
	g.GET("/database/columns", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		table := c.Query("table")
		tables, err := a.DB.Migrator().GetTables()
		if err != nil {
			response.Report(c, err)
			return
		}
		if !slices.Contains(tables, table) {
			response.Fail(c, 404, "数据表不存在")
			return
		}
		columns, err := a.DB.Migrator().ColumnTypes(table)
		if err != nil {
			response.Report(c, err)
			return
		}
		data := []gin.H{}
		for _, column := range columns {
			nullable, _ := column.Nullable()
			primary, _ := column.PrimaryKey()
			comment, _ := column.Comment()
			defaultValue, _ := column.DefaultValue()
			data = append(data, gin.H{"name": column.Name(), "type": column.DatabaseTypeName(), "nullable": nullable, "primary": primary, "comment": comment, "default": defaultValue})
		}
		response.Respond(c, data)
	})
	g.POST("/database/maintain", middleware.Require(a.App, "dawn.database.update"), func(c *gin.Context) {
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
	g.GET("/database/recycle", middleware.Require(a.App, "dawn.database.list"), func(c *gin.Context) {
		if !middleware.Actor(c).User.Super {
			response.Fail(c, 403, "回收站仅超级管理员可操作")
			return
		}
		model, ok := recyclable[c.Query("resource")]
		if !ok {
			response.Fail(c, 422, "资源未注册")
			return
		}
		var rows []map[string]any
		if err := a.DB.Unscoped().Model(model).Where("deleted_at > 0").Limit(200).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		for _, row := range rows {
			delete(row, "password")
		}
		response.Respond(c, rows)
	})
	g.POST("/database/recycle/:id/restore", middleware.Require(a.App, "dawn.database.update"), func(c *gin.Context) {
		if !middleware.Actor(c).User.Super {
			response.Fail(c, 403, "回收站仅超级管理员可操作")
			return
		}
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		model, ok := recyclable[c.Query("resource")]
		if !ok {
			response.Fail(c, 422, "资源未注册")
			return
		}
		if err := a.DB.Unscoped().Model(model).Where("id = ? AND deleted_at > 0", id).Update("deleted_at", 0).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})

}

var recyclable = map[string]any{"users": &models.User{}, "roles": &models.Role{}, "departments": &models.Department{}, "posts": &models.Post{}, "attachments": &models.Attachment{}, "categories": &models.Category{}, "menus": &models.Menu{}}
var identifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
