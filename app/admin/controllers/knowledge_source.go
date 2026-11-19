package controllers

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *Controller) knowledgeSourceRoutes(g *route.Registrar, scope func(*gorm.DB, *admin.Actor) *gorm.DB) {
	handlers := ResourceHandlers{Name: "dawn.knowledge.sources"}
	query := func(c *gin.Context, db *gorm.DB) *gorm.DB {
		return scope(db.WithContext(c.Request.Context()).Model(&models.KnowledgeSource{}), middleware.Actor(c))
	}
	handlers.Index = func(c *gin.Context) {
		q := query(c, a.DB)
		for _, field := range []string{"app_id", "kind", "status"} {
			if value := c.Query(field); value != "" {
				q = q.Where(field+" = ?", value)
			}
		}
		if name := c.Query("name"); name != "" {
			q = q.Where("name LIKE ?", "%"+name+"%")
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.KnowledgeSource{}
		if err := q.Order(response.Order(c, "id", "name", "updated_at")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	}
	handlers.Show = func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var source models.KnowledgeSource
		if err := query(c, a.DB).First(&source, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, source)
	}
	write := func(c *gin.Context) {
		id := uint(0)
		if c.Request.Method != "POST" {
			id = response.ID(c)
			if id == 0 {
				return
			}
		}
		var source models.KnowledgeSource
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if id != 0 {
				if err := query(c, tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, id).Error; err != nil {
					return err
				}
				if source.Status == "queued" || source.Status == "processing" {
					return customer.ErrConflict
				}
			}
			previous := source
			fields := []string{"name", "kind", "content"}
			if id == 0 {
				fields = append(fields, "app_id")
			}
			submitted := BindResourceInput(a.DB, c, &source, fields)
			if submitted == nil {
				return customer.ErrInvalid
			}
			if c.Request.Method != "PATCH" {
				for _, field := range fields {
					if !slices.Contains(submitted, field) {
						response.Fail(c, 422, "缺少必填字段: %s", field)
						return customer.ErrInvalid
					}
				}
			}
			if source.AppID == 0 || strings.TrimSpace(source.Name) == "" || utf8.RuneCountInString(source.Name) > 200 || strings.TrimSpace(source.Content) == "" || !slices.Contains([]string{"text", "markdown", "url"}, source.Kind) {
				return customer.ErrInvalid
			}
			var app models.SupportApp
			if err := tx.Scopes(middleware.Actor(c).Scope("created_by", "")).First(&app, source.AppID).Error; err != nil {
				return err
			}
			if id == 0 {
				return tx.Create(&source).Error
			}
			if source.Name != previous.Name || source.Kind != previous.Kind || source.Content != previous.Content {
				source.Status, source.Error = "draft", ""
				submitted = append(submitted, "status", "error")
			}
			if c.Request.Method == "PUT" {
				submitted = append(fields, "status", "error")
			}
			return query(c, tx).Where("id = ?", id).Select(submitted).Updates(&source).Error
		})
		if c.IsAborted() {
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		if id == 0 {
			response.Created(c, source, a.Options.AdminPrefix+"/knowledge-sources/"+strconv.FormatUint(uint64(source.ID), 10))
		} else {
			response.Respond(c, source)
		}
	}
	handlers.Store, handlers.Update = write, write
	handlers.Destroy = func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			q := query(c, tx).Where("id IN ?", ids)
			var count int64
			if err := q.Session(&gorm.Session{}).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return q.Delete(&models.KnowledgeSource{}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}

	RegisterResourceRoutes(a.App, g, "/knowledge-sources", "dawn.knowledge", handlers)
}
