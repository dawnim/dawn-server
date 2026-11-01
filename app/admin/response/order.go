package response

import (
	"slices"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// Order 仅允许列表已公开的字段参与排序.
func Order(c *gin.Context, fields ...string) clause.OrderByColumn {
	field := c.Query("orderField")
	if !slices.Contains(fields, field) {
		return clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}
	}
	return clause.OrderByColumn{Column: clause.Column{Name: field}, Desc: c.Query("orderType") != "asc"}
}
