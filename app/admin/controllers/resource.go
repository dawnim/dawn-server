package controllers

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

// Resource 只描述重复的CRUD，业务约束由所属模块提供。
type Resource[T any] struct {
	Path, Permission         string
	Fields, Search, Required []string
	Tree, Importable         bool
	Scope                    func(*gorm.DB, *admin.Actor) *gorm.DB
	WriteScope               func(*gorm.DB, *admin.Actor) *gorm.DB // 目录可读，写入另受管理级别限制
	AfterWrite               func(context.Context) error           // 仅在写入成功、事务提交后执行
	Validate                 func(*T, *admin.Actor, bool) error
	Prepare                  func(*T, *admin.Actor, bool)
}

func RegisterResource[T any](a *admin.App, g *route.Registrar, r Resource[T]) {
	query := func(c *gin.Context, writing bool) *gorm.DB {
		db := a.DB.WithContext(c.Request.Context()).Model(new(T))
		if r.Scope != nil {
			db = r.Scope(db, middleware.Actor(c))
		}
		if writing && r.WriteScope != nil {
			db = r.WriteScope(db, middleware.Actor(c))
		}
		return db
	}
	filtered := func(c *gin.Context) *gorm.DB {
		db := query(c, false)
		for _, field := range r.Search {
			if value := c.Query(field); value != "" {
				db = db.Where(field+" LIKE ?", "%"+value+"%")
			}
		}
		if value := c.Query("status"); value != "" && slices.Contains(r.Fields, "status") {
			db = db.Where("status = ?", value)
		}
		return db
	}
	writable := slices.DeleteFunc(slices.Clone(r.Fields), func(field string) bool { return field == "created_by" || field == "department_id" })
	writeCSV := RegisterResourceFiles(a, g, r, filtered)
	g.Get(r.Path, middleware.Require(a, r.Permission+".list"), func(c *gin.Context) {
		if c.NegotiateFormat("application/json", "text/csv") == "text/csv" {
			if !middleware.Actor(c).Can(r.Permission + ".export") {
				response.Fail(c, 403, "没有操作权限")
				return
			}
			writeCSV(c)
			return
		}
		db := filtered(c)
		var total int64
		if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		if !r.Tree {
			db = db.Offset((page - 1) * size).Limit(size)
		}
		rows := []T{}
		order := response.Order(c, append(slices.Clone(r.Fields), "id", "created_at", "updated_at")...)
		if err := db.Order(order).Order("id DESC").Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		if r.Tree {
			response.Respond(c, rows)
		} else {
			response.Paginated(c, rows, total, page, size)
		}
	}).Name(r.Permission + ".index")
	g.Get(r.Path+"/{id}", middleware.Require(a, r.Permission+".list"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var row T
		if err := query(c, false).First(&row, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, row)
	}).Name(r.Permission + ".show")
	g.Post(r.Path, middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
		var row T
		submitted := BindResourceInput(a.DB, c, &row, writable)
		if submitted == nil {
			return
		}
		for _, field := range r.Required {
			if !slices.Contains(submitted, field) {
				response.Fail(c, 422, "缺少必填字段: %s", field)
				return
			}
		}
		if r.Prepare != nil {
			r.Prepare(&row, middleware.Actor(c), false)
		}
		if r.Validate != nil {
			if err := r.Validate(&row, middleware.Actor(c), false); err != nil {
				response.Report(c, err, 422)
				return
			}
		}
		if err := CreateResource(a.DB.WithContext(c.Request.Context()), &row, r.Fields, submitted); err != nil {
			response.Report(c, err)
			return
		}
		if r.AfterWrite != nil {
			if err := r.AfterWrite(c.Request.Context()); err != nil {
				response.Report(c, err)
				return
			}
		}
		stmt := &gorm.Statement{DB: a.DB}
		_ = stmt.Parse(&row)
		id, _ := stmt.Schema.PrioritizedPrimaryField.ValueOf(c.Request.Context(), reflect.ValueOf(&row))
		response.Created(c, row, fmt.Sprintf("%s/%v", c.Request.URL.Path, id))
	}).Name(r.Permission + ".store")
	update := func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var row T
		if err := query(c, true).First(&row, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		selected := BindResourceInput(a.DB, c, &row, writable)
		if selected == nil {
			return
		}
		if c.Request.Method == "PUT" {
			for _, field := range r.Required {
				if !slices.Contains(selected, field) {
					response.Fail(c, 422, "缺少必填字段: %s", field)
					return
				}
			}
			selected = writable
		}
		if r.Prepare != nil {
			r.Prepare(&row, middleware.Actor(c), true)
		}
		if r.Validate != nil {
			if err := r.Validate(&row, middleware.Actor(c), true); err != nil {
				response.Report(c, err, 422)
				return
			}
		}
		if err := query(c, true).Where("id = ?", id).Select(selected).Updates(&row).Error; err != nil {
			response.Report(c, err)
			return
		}
		if r.AfterWrite != nil {
			if err := r.AfterWrite(c.Request.Context()); err != nil {
				response.Report(c, err)
				return
			}
		}
		response.Respond(c, row)
	}
	g.Match([]string{"PUT", "PATCH"}, r.Path+"/{id}", middleware.Require(a, r.Permission+".update"), update).Name(r.Permission + ".update")
	remove := func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			db := tx.Model(new(T))
			if r.Scope != nil {
				db = r.Scope(db, middleware.Actor(c))
			}
			if r.WriteScope != nil {
				db = r.WriteScope(db, middleware.Actor(c))
			}
			var total int64
			if err := db.Where("id IN ?", ids).Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			if total != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return db.Where("id IN ?", ids).Delete(new(T)).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		if r.AfterWrite != nil {
			if err := r.AfterWrite(c.Request.Context()); err != nil {
				response.Report(c, err)
				return
			}
		}
		c.Status(204)
	}
	g.Delete(r.Path, middleware.Require(a, r.Permission+".delete"), remove).Name(r.Permission + ".destroy-many")
	g.Delete(r.Path+"/{id}", middleware.Require(a, r.Permission+".delete"), remove).Name(r.Permission + ".destroy")
}
