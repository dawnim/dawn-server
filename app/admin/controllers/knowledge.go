package controllers

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
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

func (a *Controller) knowledgeRows(c *gin.Context) *gorm.DB {
	return a.DB.WithContext(c.Request.Context()).Model(&models.KnowledgeEntry{}).Where("app_id IN (?)", a.supportApps(c).Select("id"))
}

func (a *Controller) KnowledgeRoutes(g *route.Registrar) {
	handlers := ResourceHandlers{}
	g.Post("/support-apps/{id}/knowledge-drafts", middleware.Require(a.App, "dawn.knowledge.create"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		var input struct {
			Content string `json:"content"`
			Kind    string `json:"kind"`
		}
		if c.ShouldBindJSON(&input) != nil || input.Content == "" {
			response.Report(c, customer.ErrInvalid)
			return
		}
		content, err := customer.FetchKnowledge(c.Request.Context(), input.Kind, input.Content)
		if err != nil {
			response.Report(c, err)
			return
		}
		entries, err := a.Customer.GenerateKnowledge(c.Request.Context(), id, content)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, entries)
	})

	g.Get("/knowledge-experts", middleware.Require(a.App, "dawn.knowledge.list"), func(c *gin.Context) {
		q := a.DB.WithContext(c.Request.Context()).Model(&models.User{}).Where("telegram_id > 0 AND status = ?", models.Enabled)
		if query := c.Query("query"); query != "" {
			q = q.Where("nickname LIKE ? OR username LIKE ? OR telegram_username LIKE ?", "%"+query+"%", "%"+query+"%", "%"+query+"%")
		}
		if c.Query("ids") != "" {
			ids := response.IDs(c)
			if c.IsAborted() {
				return
			}
			q = q.Where("id IN ?", ids)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.User{}
		if err := q.Select("id", "unique_id", "nickname", "username", "telegram_username").Order("id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	})
	g.Get("/knowledge-apps", middleware.Require(a.App, "dawn.knowledge.list"), func(c *gin.Context) {
		q := a.supportApps(c)
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		}{}
		if err := q.Select("id", "name").Order("id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	})
	scope := func(db *gorm.DB, actor *admin.Actor) *gorm.DB {
		return db.Where("app_id IN (?)", db.Session(&gorm.Session{NewDB: true}).Model(&models.SupportApp{}).Scopes(actor.Scope("created_by", "")).Select("id"))
	}
	a.knowledgeSourceRoutes(g, scope)
	g.Post("/knowledge-entry-batches", middleware.Require(a.App, "dawn.knowledge.create"), func(c *gin.Context) {
		var input struct {
			Entries []map[string]json.RawMessage `json:"entries"`
		}
		if c.ShouldBindJSON(&input) != nil || len(input.Entries) == 0 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		rows := make([]models.KnowledgeEntry, len(input.Entries))
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			for i, raw := range input.Entries {
				for key := range raw {
					if (!slices.Contains(knowledgeFields, key) && key != "app_id" && key != "source_id") || (string(raw[key]) == "null" && key != "experts") {
						return customer.ErrInvalid
					}
				}
				row := &rows[i]
				row.Status, row.ReplyMode, row.Locale, row.Version = "published", "reference", "zh", 1
				if json.Unmarshal(customer.Raw(raw), row) != nil {
					return customer.ErrInvalid
				}
				if err := a.validateKnowledge(c, tx, row); err != nil {
					return err
				}
			}
			return tx.Create(&rows).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, rows, a.Options.AdminPrefix+"/knowledge-entries")
	})
	handlers.Index = func(c *gin.Context) {
		q := a.knowledgeRows(c)
		if id := c.Query("app_id"); id != "" {
			q = q.Where("app_id = ?", id)
		}
		if sourceID := c.Query("source_id"); sourceID != "" {
			q = q.Where("source_id = ?", sourceID)
		}
		if status := c.Query("status"); status != "" {
			q = q.Where("status = ?", status)
		}
		if query := c.Query("query"); query != "" {
			q = q.Where("question LIKE ? OR keywords LIKE ? OR answer LIKE ?", "%"+query+"%", "%"+query+"%", "%"+query+"%")
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.KnowledgeEntry{}
		if err := q.Order(response.Order(c, "id", "question", "updated_at")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
		var entry models.KnowledgeEntry
		if err := a.knowledgeRows(c).First(&entry, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, entry)
	}
	write := func(c *gin.Context) {
		var entry models.KnowledgeEntry
		if c.Request.Method != "POST" {
			id := response.ID(c)
			if id == 0 {
				return
			}
			if err := a.knowledgeRows(c).First(&entry, id).Error; err != nil {
				response.Report(c, err)
				return
			}
		}
		version := entry.Version
		fields := slices.Clone(knowledgeFields)
		if entry.ID == 0 {
			fields = append(fields, "app_id", "source_id")
		} else {
			fields = append(fields, "version")
		}
		submitted := BindResourceInput(a.DB, c, &entry, fields)
		if submitted == nil {
			return
		}
		if c.Request.Method == "PUT" {
			for _, required := range []string{"question", "answer", "status", "reply_mode", "version"} {
				if !slices.Contains(submitted, required) {
					response.Report(c, customer.ErrInvalid)
					return
				}
			}
			submitted = fields
		}
		if err := a.validateKnowledge(c, a.DB.WithContext(c.Request.Context()), &entry); err != nil {
			response.Report(c, err)
			return
		}
		if entry.ID == 0 {
			entry.Version = 1
			if err := a.DB.WithContext(c.Request.Context()).Create(&entry).Error; err != nil {
				response.Report(c, err)
				return
			}
			response.Created(c, entry, c.Request.URL.Path+"/"+strconv.FormatUint(uint64(entry.ID), 10))
			return
		}
		if !slices.Contains(submitted, "version") || entry.Version != version {
			response.Report(c, customer.ErrConflict)
			return
		}
		entry.Version++
		result := a.knowledgeRows(c).Where("id = ? AND version = ?", entry.ID, version).Select(submitted).Updates(&entry)
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected != 1 {
			response.Report(c, customer.ErrConflict)
			return
		}
		response.Respond(c, entry)
	}
	handlers.Store, handlers.Update = write, write
	handlers.Destroy = func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			q := scope(tx.Model(&models.KnowledgeEntry{}), middleware.Actor(c)).Where("id IN ?", ids)
			var count int64
			if err := q.Session(&gorm.Session{}).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return q.Delete(&models.KnowledgeEntry{}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}

	g.Post("/knowledge-sources/{id}/imports", middleware.Require(a.App, "dawn.knowledge.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var run models.AIRun
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var source models.KnowledgeSource
			if err := scope(tx.Clauses(clause.Locking{Strength: "UPDATE"}), middleware.Actor(c)).First(&source, id).Error; err != nil {
				return err
			}
			if source.Status == "queued" || source.Status == "processing" {
				return customer.ErrConflict
			}
			run = models.AIRun{AppID: source.AppID, Kind: "import", Input: customer.Raw(map[string]any{"source_id": source.ID}), DeadlineAt: time.Now().Add(6 * time.Hour).UnixMilli()}
			if err := tx.Create(&run).Error; err != nil {
				return err
			}
			return tx.Model(&source).Updates(map[string]any{"status": "queued", "error": ""}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		a.WakeChat(c.Request.Context())
		response.Created(c, run, a.Options.AdminPrefix+"/ai-runs/"+strconv.FormatUint(uint64(run.ID), 10))
	})
	g.Post("/support-apps/{id}/index-builds", middleware.Require(a.App, "dawn.knowledge.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		run := models.AIRun{AppID: id, Kind: "index", DeadlineAt: time.Now().Add(6 * time.Hour).UnixMilli()}
		if err := a.DB.WithContext(c.Request.Context()).Create(&run).Error; err != nil {
			response.Report(c, err)
			return
		}
		a.WakeChat(c.Request.Context())
		response.Created(c, run, a.Options.AdminPrefix+"/ai-runs/"+strconv.FormatUint(uint64(run.ID), 10))
	})
	runs := ResourceHandlers{Name: "dawn.knowledge.runs"}
	runs.Index = func(c *gin.Context) {
		q := scope(a.DB.WithContext(c.Request.Context()).Model(&models.AIRun{}), middleware.Actor(c))
		if id := c.Query("app_id"); id != "" {
			q = q.Where("app_id = ?", id)
		}
		if kind := c.Query("kind"); kind != "" {
			q = q.Where("kind IN ?", strings.Split(kind, ","))
		}
		if status := c.Query("status"); status != "" {
			q = q.Where("status = ?", status)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.AIRun{}
		if err := q.Select("id", "app_id", "conversation_id", "message_id", "reply_id", "parent_id", "kind", "status", "version", "attempt", "available_at", "deadline_at", "error", "created_at", "updated_at").Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	}
	runs.Show = func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var run models.AIRun
		if err := scope(a.DB.WithContext(c.Request.Context()), middleware.Actor(c)).Omit("input", "output").First(&run, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, run)
	}
	g.Post("/support-apps/{id}/knowledge-searches", middleware.Require(a.App, "dawn.knowledge.list"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		var input struct {
			Query string `json:"query"`
		}
		if c.ShouldBindJSON(&input) != nil || input.Query == "" {
			response.Report(c, customer.ErrInvalid)
			return
		}
		rows, err := a.Customer.Search(c.Request.Context(), id, input.Query)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, rows)
	})
	RegisterResourceRoutes(a.App, g, "/ai-runs", "dawn.knowledge", runs)
	RegisterResourceRoutes(a.App, g, "/knowledge-entries", "dawn.knowledge", handlers)
}

var knowledgeFields = []string{"question", "answer", "keywords", "conditions", "locale", "reply_mode", "status", "experts", "location"}

func (a *Controller) validateKnowledge(c *gin.Context, db *gorm.DB, entry *models.KnowledgeEntry) error {
	if entry.AppID == 0 || strings.TrimSpace(entry.Question) == "" || strings.TrimSpace(entry.Answer) == "" || utf8.RuneCountInString(entry.Question) > 1000 || utf8.RuneCountInString(entry.Location) > 1000 || utf8.RuneCountInString(entry.Locale) > 16 {
		return customer.ErrInvalid
	}
	if !slices.Contains([]string{"draft", "published", "disabled"}, entry.Status) || !slices.Contains([]string{"fixed", "reference"}, entry.ReplyMode) {
		return customer.ErrInvalid
	}
	var expertIDs []uint
	if len(entry.Experts) > 0 && json.Unmarshal(entry.Experts, &expertIDs) != nil {
		return customer.ErrInvalid
	}
	if len(expertIDs) > 0 {
		ids := slices.Compact(slices.Sorted(slices.Values(expertIDs)))
		var count int64
		if err := db.Model(&models.User{}).Where("id IN ? AND telegram_id > 0 AND status = ?", ids, models.Enabled).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return customer.ErrInvalid
		}
	}
	var app models.SupportApp
	if err := db.Scopes(middleware.Actor(c).Scope("created_by", "")).First(&app, entry.AppID).Error; err != nil {
		return err
	}
	if entry.SourceID != 0 {
		var source models.KnowledgeSource
		if err := db.Where("app_id = ?", entry.AppID).First(&source, entry.SourceID).Error; err != nil {
			return err
		}
	}
	return nil
}
