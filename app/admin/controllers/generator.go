package controllers

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/support/codegen"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/route"
	"github.com/prismgo/framework/support"
	"gorm.io/gorm"
)

func (a *Controller) GeneratorRoutes(g *route.Registrar) {
	g.Get("/code/tables", middleware.Require(a.App, "dawn.generator.list"), func(c *gin.Context) {
		tables, err := codegen.Tables(a.DB.WithContext(c.Request.Context()))
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, tables)
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
		options := codegen.Options{Table: c.Query("table"), Resource: c.Query("resource"), Directory: c.Query("directory"), Mode: c.Query("mode"), Module: config.GetString("dawn.module"), Migration: c.Query("migration") == "1"}
		files, err := codegen.Generate(a.DB.WithContext(c.Request.Context()), options)
		if err != nil {
			var invalid i18n.Error
			if errors.As(err, &invalid) {
				response.Fail(c, 422, invalid.Key, invalid.Args...)
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				response.Fail(c, 404, "数据表不存在")
			} else {
				response.Report(c, err)
			}
			return
		}
		// Keep the timestamp of the migration already edited in the preview.
		for name, data := range files {
			if strings.HasPrefix(name, "database/migrations/") {
				for edited := range input.Files {
					if matched, _ := regexp.MatchString(`^database/migrations/[0-9]{14}_create_`+options.Table+`\.go$`, edited); matched {
						delete(files, name)
						files[edited] = data
						break
					}
				}
			}
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
			created, err := codegen.Write(support.BasePath(), files)
			if err != nil {
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
