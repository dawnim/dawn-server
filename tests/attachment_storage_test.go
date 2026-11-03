package tests

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
)

func TestAttachmentStorage(t *testing.T) {
	root := t.TempDir()
	redis := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: filepath.Join(root, "test.sqlite"), Storage: root, RedisURL: "redis://" + redis.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// 安装命令创建六种系统分类；不依赖请求时修补数据。
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	token := "attachment-test-token"
	if err := a.DB.Create(&models.Session{ID: "attachment-test-session", UserID: 1, AccessHash: admin.TokenHash(token), RefreshHash: admin.TokenHash("refresh"), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "public"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "app/public"), filepath.Join(root, "public/storage")); err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	setting := func(key, value string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{key: value})
		req := httptest.NewRequest("PATCH", "/admin/settings", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
	}
	upload := func(name string, content []byte, category string) models.Attachment {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(content); err != nil {
			t.Fatal(err)
		}
		md5sum, sha := md5.Sum(content), sha256.Sum256(content)
		for field, value := range map[string]string{"md5": hex.EncodeToString(md5sum[:]), "sha256": hex.EncodeToString(sha[:]), "category_id": category} {
			if value != "" {
				if err := writer.WriteField(field, value); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/admin/attachments", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		var response struct {
			Data struct {
				ID  uint
				URL string
			}
		}
		if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var row models.Attachment
		if err := a.DB.Preload("File").First(&row, response.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.File.Location.Visibility == "public" && response.Data.URL != "/storage/"+row.File.Path {
			t.Fatal("公开地址错误", response.Data.URL)
		}
		return row
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, kind string
		content    []byte
	}{
		{"photo.PNG", "image", picture.Bytes()}, {"manual.pdf", "document", []byte("%PDF-1.4\nfixture")},
		{"archive.zip", "archive", []byte{'P', 'K', 3, 4, 1, 2}}, {"sound.wav", "audio", append([]byte("RIFF1234WAVEfmt "), make([]byte, 64)...)},
		{"video.webm", "video", []byte{0x1a, 0x45, 0xdf, 0xa3, 0x9f, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}}, {"unknown.bin", "other", []byte{1, 2, 3, 4, 5}},
	} {
		if item.kind == "other" {
			setting("upload.extensions", "png,pdf,zip,wav,webm,bin")
		}
		row := upload(item.name, item.content, "")
		var category models.Category
		if err := a.DB.First(&category, row.CategoryID).Error; err != nil {
			t.Fatal(err)
		}
		if category.Code == nil || *category.Code != item.kind {
			t.Fatal(item.name, category)
		}
		if filepath.Ext(row.File.Path) != strings.ToLower(filepath.Ext(item.name)) {
			t.Fatal("扩展名丢失", row.File.Path)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", "/storage/"+row.File.Path, nil))
		if res.Code != 200 || !bytes.Equal(res.Body.Bytes(), item.content) {
			t.Fatal("公开URL不可访问", res.Code)
		}
		actual, err := os.ReadFile(filepath.Join(root, "app/public", row.File.Path))
		if err != nil || !bytes.Equal(actual, item.content) {
			t.Fatal("公开磁盘文件不符", err)
		}
	}
	setting("upload.visibility", "private")
	row := upload("private.pdf", []byte("%PDF-1.4\nprivate"), "")
	if row.File.Location.Visibility != "private" || row.File.Location.Root != filepath.Join(root, "uploads") {
		t.Fatal(row.File.Location)
	}
	for _, authorized := range []bool{false, true} {
		req := httptest.NewRequest("GET", "/admin/attachments/"+fmt.Sprint(row.ID)+"/content", nil)
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		want := 401
		if authorized {
			want = 200
		}
		if res.Code != want {
			t.Fatal(res.Code, res.Body.String())
		}
	}
}
