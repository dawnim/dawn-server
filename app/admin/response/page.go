package response

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Page 保留调用方指定的条数，不设置固定上限。
func Page(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	return max(1, page), max(1, size)
}

func Paginated(c *gin.Context, data any, total int64, page, size int) {
	c.JSON(200, gin.H{"data": data, "meta": gin.H{"total": total, "page": page, "page_size": size}})
}

// IDs 接受单条路径ID或集合上的显式ID列表，不接受整集合删除。
func IDs(c *gin.Context) []uint {
	values := c.Query("ids")
	if c.Param("id") != "" {
		values = c.Param("id")
	}
	ids := []uint{}
	for _, value := range strings.Split(values, ",") {
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			Fail(c, 422, "无效的记录ID")
			return nil
		}
		ids = append(ids, uint(id))
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}
