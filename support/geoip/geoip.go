// Package geoip 提供本地IP地理查询及原子更新。
package geoip

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/support"
)

var reader struct {
	sync.Mutex
	db       *maxminddb.Reader
	path     string
	modified time.Time
}

func Path() string {
	return config.GetString("dawn.geoip.path", support.StoragePath("app", "geoip", "GeoLite2-City.mmdb"))
}

// Update 下载并验证新文件，失败时保留当前可用数据库。
func Update(ctx context.Context) error {
	if !config.GetBool("dawn.geoip.enabled", true) {
		return nil
	}
	target := Path()
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	url := config.GetString("dawn.geoip.url", "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if stat, err := os.Stat(target); err == nil {
		request.Header.Set("If-Modified-Since", stat.ModTime().UTC().Format(http.TimeFormat))
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GeoIP download: HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(filepath.Dir(target), "geoip-*.mmdb")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	size, err := io.Copy(file, io.LimitReader(response.Body, 128<<20+1))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if size > 128<<20 {
		return fmt.Errorf("GeoIP database exceeds 128MB")
	}
	db, err := maxminddb.Open(file.Name())
	if err != nil {
		return err
	}
	valid := strings.Contains(db.Metadata.DatabaseType, "City")
	err = db.Verify()
	db.Close()
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("GeoIP database is not a City database")
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return err
	}
	if modified, err := http.ParseTime(response.Header.Get("Last-Modified")); err == nil {
		return os.Chtimes(target, time.Now(), modified)
	}
	return nil
}

// Lookup 返回国家代码和地理名称；内网地址不查询公网数据库。
func Lookup(ip string) (string, string, error) {
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", err
	}
	if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return "", "内网地址", nil
	}
	if !config.GetBool("dawn.geoip.enabled", true) {
		return "", "", nil
	}
	reader.Lock()
	defer reader.Unlock()
	target := Path()
	stat, err := os.Stat(target)
	if err != nil {
		return "", "", err
	}
	if reader.db == nil || reader.path != target || !stat.ModTime().Equal(reader.modified) {
		db, err := maxminddb.Open(target)
		if err != nil {
			return "", "", err
		}
		if reader.db != nil {
			reader.db.Close()
		}
		reader.db, reader.path, reader.modified = db, target, stat.ModTime()
	}
	type names struct {
		Names map[string]string `maxminddb:"names"`
	}
	var record struct {
		Country struct {
			ISO   string            `maxminddb:"iso_code"`
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
		Subdivisions []names `maxminddb:"subdivisions"`
		City         names   `maxminddb:"city"`
	}
	if err := reader.db.Lookup(address).Decode(&record); err != nil {
		return "", "", err
	}
	parts := []string{cmp.Or(record.Country.Names["zh-CN"], record.Country.Names["en"])}
	if len(record.Subdivisions) > 0 {
		parts = append(parts, cmp.Or(record.Subdivisions[0].Names["zh-CN"], record.Subdivisions[0].Names["en"]))
	}
	parts = append(parts, cmp.Or(record.City.Names["zh-CN"], record.City.Names["en"]))
	return record.Country.ISO, strings.TrimSpace(strings.Join(parts, " ")), nil
}
