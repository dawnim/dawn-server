package response

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func Page(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	return max(1, page), min(200, max(1, size))
}
