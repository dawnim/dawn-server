package response

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestPageDefaultsAndExplicitSize(t *testing.T) {
	for _, test := range []struct {
		query      string
		page, size int
	}{{"", 1, 10}, {"?page=2&page_size=35", 2, 35}, {"?page_size=300", 1, 300}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/records"+test.query, nil)
		page, size := Page(c)
		if page != test.page || size != test.size {
			t.Fatalf("%s: got %d/%d", test.query, page, size)
		}
	}
}
