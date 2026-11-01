package controllers

import (
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
)

func (a *Controller) DashboardRoutes(g *gin.RouterGroup) {
	g.GET("/dashboard/statistics", middleware.Require(a.App, ""), func(c *gin.Context) {
		actor := middleware.Actor(c)
		counts := gin.H{}
		for _, item := range []struct {
			key               string
			model             any
			owner, department string
		}{
			{"user", &models.User{}, "id", "department_id"},
			{"attach", &models.Attachment{}, "created_by", ""},
		} {
			var count int64
			if err := a.DB.Model(item.model).Scopes(actor.Scope(item.owner, item.department)).Count(&count).Error; err != nil {
				response.Report(c, err)
				return
			}
			counts[item.key] = count
		}
		for _, kind := range []string{"login", "operation"} {
			var count int64
			if err := a.DB.Model(&models.Log{}).Scopes(actor.Scope("user_id", "")).Where("kind = ?", kind).Count(&count).Error; err != nil {
				response.Report(c, err)
				return
			}
			counts[map[string]string{"login": "login", "operation": "operate"}[kind]] = count
		}
		response.Respond(c, counts)
	})
	for _, period := range []string{"daily", "monthly"} {
		g.GET("/dashboard/"+period, middleware.Require(a.App, ""), func(c *gin.Context) {
			now := time.Now().UTC()
			start, layout, step := now.AddDate(0, 0, -13), "01-02", 0
			if period == "monthly" {
				start, layout, step = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0), "2006-01", 1
			}
			start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
			var timestamps []int64
			if err := a.DB.Model(&models.Log{}).Scopes(middleware.Actor(c).Scope("user_id", "")).Where("kind = ? AND status < ? AND created_at >= ?", "login", 400, start.UnixMilli()).Pluck("created_at", &timestamps).Error; err != nil {
				response.Report(c, err)
				return
			}
			counts := map[string]int{}
			for _, timestamp := range timestamps {
				counts[time.UnixMilli(timestamp).UTC().Format(layout)]++
			}
			labels, values := []string{}, []int{}
			for date := start; !date.After(now); date = date.AddDate(0, step, 1-step) {
				label := date.Format(layout)
				labels, values = append(labels, label), append(values, counts[label])
			}
			key := "login_date"
			if period == "monthly" {
				key = "login_month"
			}
			response.Respond(c, gin.H{key: labels, "login_count": values})
		})
	}
}
