package controllers

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/horizon"
)

func (a *Controller) HorizonRoutes(g *gin.RouterGroup) {
	fields := regexp.MustCompile(`"[A-Z][A-Za-z0-9]*":`)
	g.GET("/system/horizon/:view", middleware.Require(a.App, "dawn.horizon.list"), func(c *gin.Context) {
		store, err := horizon.Resolve().ResolveStore(c.Request.Context())
		if err != nil {
			response.Report(c, err)
			return
		}
		var value any
		ctx, now := c.Request.Context(), time.Now()
		switch c.Param("view") {
		case "status":
			value, err = store.StatusSnapshot(ctx, now)
		case "masters":
			value, err = store.Masters(ctx, now)
		case "supervisors":
			value, err = store.Supervisors(ctx, now)
		case "workers":
			value, err = store.Workers(ctx, now)
		case "queues":
			value, err = store.QueueLengthSnapshot(ctx)
		case "metrics":
			value, err = store.EventMetricWindows(ctx, horizon.EventMetricWindowQuery{})
		case "details":
			value, err = store.HighValueDetails(ctx, horizon.HighValueDetailQuery{})
		default:
			c.Status(404)
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		// 上游状态结构未声明JSON字段名，在后台接口边界统一使用现有ORM命名策略。
		data, err := json.Marshal(value)
		if err != nil {
			response.Report(c, err)
			return
		}
		data = fields.ReplaceAllFunc(data, func(field []byte) []byte {
			name := a.DB.NamingStrategy.ColumnName("", string(field[1:len(field)-2]))
			return []byte(`"` + name + `":`)
		})
		response.Respond(c, json.RawMessage(data))
	})
}
