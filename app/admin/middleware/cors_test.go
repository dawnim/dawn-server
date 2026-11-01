package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name               string
		origins            []string
		origin             string
		status             int
		allow, credentials string
	}{
		{"all", []string{"*"}, "https://any.example", 204, "*", ""},
		{"allowed", []string{"https://admin.example"}, "https://admin.example", 204, "https://admin.example", "true"},
		{"denied", []string{"https://admin.example"}, "https://other.example", 403, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(CORS(tc.origins))
			router.GET("/api", func(c *gin.Context) { c.Status(200) })
			req := httptest.NewRequest("OPTIONS", "/api", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status || response.Header().Get("Access-Control-Allow-Origin") != tc.allow || response.Header().Get("Access-Control-Allow-Credentials") != tc.credentials {
				t.Fatalf("CORS响应: status=%d headers=%v", response.Code, response.Header())
			}
		})
	}
}
