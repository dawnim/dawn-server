package storage

import (
	"bytes"
	"context"
	"github.com/prismgo/framework/filesystem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 用真实SDK访问隔离协议端点，覆盖配置选择、签名、上传和读取。
func TestDisks(t *testing.T) {
	for _, driver := range []string{"local", "oss", "qiniu", "cos", "s3"} {
		t.Run(driver, func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.Header.Get("Authorization"), "test-access") {
					t.Error("请求缺少SDK签名")
					w.WriteHeader(403)
					return
				}
				key := strings.TrimPrefix(r.URL.Path, "/bucket/")
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case "PUT":
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					objects[key] = data
					w.Header().Set("ETag", `"etag"`)
				case "GET", "HEAD":
					data, ok := objects[key]
					if !ok {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("Content-Type", "image/png")
					w.Header().Set("Content-Length", "7")
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					if r.Method == "GET" {
						w.Write(data)
					}
				case "DELETE":
					delete(objects, key)
					w.WriteHeader(204)
				}
			}))
			defer endpoint.Close()
			cfg := Config{Driver: driver, Root: t.TempDir(), Bucket: "bucket", Region: "us-east-1", Endpoint: endpoint.URL, Prefix: "assets", PathStyle: true, AccessKey: "test-access", SecretKey: "test-secret"}
			disk, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer disk.Close()
			ctx := context.Background()
			if err := disk.PutReader(ctx, "sample.png", bytes.NewReader([]byte("content")), filesystem.PutOptions{ContentType: "image/png"}); err != nil {
				t.Fatal(err)
			}
			exists, err := disk.Exists(ctx, "sample.png")
			if err != nil || !exists {
				t.Fatalf("文件不存在: %v", err)
			}
			content, err := disk.Get(ctx, "sample.png")
			if err != nil || string(content) != "content" {
				t.Fatalf("读取错误: %q %v", content, err)
			}
			if err := disk.Delete(ctx, "sample.png"); err != nil {
				t.Fatal(err)
			}
			exists, err = disk.Exists(ctx, "sample.png")
			if err != nil || exists {
				t.Fatalf("删除后仍然存在: %v", err)
			}
		})
	}
}
