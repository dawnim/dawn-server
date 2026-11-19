package controllers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

func (a *Controller) SkillRoutes(g *route.Registrar) {
	g.Get("/support-apps/{id}/global-skills", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		entries, err := os.ReadDir(a.Customer.SkillDirectory("", "global"))
		if err != nil && !os.IsNotExist(err) {
			response.Report(c, err)
			return
		}
		skills := []customer.Skill{}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			data, e := os.ReadFile(filepath.Join(a.Customer.SkillDirectory("", "global"), entry.Name(), "SKILL.md"))
			if e != nil {
				continue
			}
			skill, e := customer.ParseSkill(string(data))
			if e == nil {
				skill.Scope = "global"
				skill.Instructions = ""
				skills = append(skills, skill)
			}
		}
		response.Respond(c, skills)
	})

	g.Post("/support-apps/{id}/skill-proposals", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
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
			Prompt string `json:"prompt"`
			URL    string `json:"url"`
		}
		if c.ShouldBindJSON(&input) != nil || input.Prompt == "" {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if input.URL != "" {
			content, err := customer.FetchKnowledge(c.Request.Context(), "url", input.URL)
			if err != nil {
				response.Report(c, err)
				return
			}
			input.Prompt += "\n资料：\n" + content
		}
		content, err := a.Customer.ProposeSkill(c.Request.Context(), id, input.Prompt)
		if err != nil {
			response.Report(c, err)
			return
		}
		skill, err := customer.ParseSkill(content)
		if err != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		response.Respond(c, gin.H{"content": content, "name": skill.Name, "description": skill.Description})
	})

	g.Post("/support-apps/{id}/skills", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 9<<20)
		file, _, err := c.Request.FormFile("file")
		if err != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if err != nil || len(data) > 8<<20 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		files := map[string]string{}
		if bytes.HasPrefix(data, []byte("PK")) {
			archive, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if e != nil || len(archive.File) > 128 {
				response.Report(c, customer.ErrInvalid)
				return
			}
			var root string
			found := false
			for _, item := range archive.File {
				if path.Base(item.Name) == "SKILL.md" {
					if found {
						response.Report(c, customer.ErrInvalid)
						return
					}
					root = strings.TrimSuffix(item.Name, "SKILL.md")
					found = true
				}
			}
			if !found {
				response.Report(c, customer.ErrInvalid)
				return
			}
			total := 0
			for _, item := range archive.File {
				if item.FileInfo().IsDir() {
					continue
				}
				name := strings.TrimPrefix(item.Name, root)
				if !strings.HasPrefix(item.Name, root) || !fs.ValidPath(name) {
					response.Report(c, customer.ErrInvalid)
					return
				}
				reader, e := item.Open()
				if e != nil {
					response.Report(c, customer.ErrInvalid)
					return
				}
				content, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
				_ = reader.Close()
				total += len(content)
				if e != nil || len(content) > 1<<20 || total > 8<<20 {
					response.Report(c, customer.ErrInvalid)
					return
				}
				if utf8.Valid(content) {
					files[name] = string(content)
				}
			}
		} else {
			files["SKILL.md"] = string(data)
		}
		skill, err := customer.ParseSkill(files["SKILL.md"])
		if err != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		delete(files, "SKILL.md")
		skill.References = files
		var cfg customer.Config
		if err = json.Unmarshal(app.Config, &cfg); err != nil {
			response.Report(c, err)
			return
		}
		scope := c.PostForm("scope")
		if scope == "" {
			scope = "app"
		}
		if scope != "app" && scope != "global" {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if scope == "global" && !middleware.Actor(c).User.Super {
			response.Report(c, customer.ErrForbidden)
			return
		}
		skill.Scope = scope
		directory := a.Customer.SkillDirectory(app.PublicKey, scope)
		if err = os.MkdirAll(directory, 0750); err != nil {
			response.Report(c, err)
			return
		}
		target := filepath.Join(directory, skill.Name)
		if _, e := os.Lstat(target); e == nil {
			response.Report(c, customer.ErrConflict)
			return
		} else if !os.IsNotExist(e) {
			response.Report(c, e)
			return
		}
		staging, err := os.MkdirTemp(directory, ".install-")
		if err != nil {
			response.Report(c, err)
			return
		}
		defer os.RemoveAll(staging)
		files["SKILL.md"] = string(data)
		if bytes.HasPrefix(data, []byte("PK")) {
			files["SKILL.md"] = "---\nname: " + skill.Name + "\ndescription: " + strconv.Quote(skill.Description) + "\nallowed-tools: " + strconv.Quote(skill.AllowedTools) + "\n---\n" + skill.Instructions
		}
		for name, content := range files {
			if !fs.ValidPath(name) {
				response.Report(c, customer.ErrInvalid)
				return
			}
			file := filepath.Join(staging, filepath.FromSlash(name))
			if err = os.MkdirAll(filepath.Dir(file), 0750); err == nil {
				err = os.WriteFile(file, []byte(content), 0640)
			}
			if err != nil {
				response.Report(c, err)
				return
			}
		}
		if err = os.Rename(staging, target); err != nil {
			response.Report(c, err)
			return
		}
		installed := false
		defer func() {
			if !installed {
				_ = os.RemoveAll(target)
			}
		}()
		skill.Instructions = ""
		skill.References = nil
		replaced := false
		for i := range cfg.Skills {
			if cfg.Skills[i].Name == skill.Name {
				cfg.Skills[i] = skill
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Skills = append(cfg.Skills, skill)
		}
		if err = customer.ValidateConfig(cfg); err != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		result := a.supportApps(c).Where("id = ? AND revision = ?", app.ID, app.Revision).Updates(map[string]any{"config": customer.Raw(cfg), "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected != 1 {
			response.Report(c, customer.ErrConflict)
			return
		}
		installed = true
		response.Created(c, skill, c.Request.URL.Path)
	})
}
