// Package geoip 提供容器管理的离线IP查询、数据库状态、原子更新和自定义解析器。
package geoip

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/prismgo/framework/container"
	cc "github.com/prismgo/framework/contracts/container"
)

// DefaultSources 可在配置中替换为自建镜像；五类文件独立验证和更新。
func DefaultSources() map[string]string {
	return map[string]string{
		"GeoLite2-City.mmdb":    "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb",
		"GeoLite2-Country.mmdb": "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb",
		"GeoLite2-ASN.mmdb":     "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-ASN.mmdb",
		"ip2region_v4.xdb":      "https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
		"ip2region_v6.xdb":      "https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v6.xdb",
	}
}

type Result struct {
	IP             string            `json:"ip"`
	CountryCode    string            `json:"country_code"`
	Country        string            `json:"country"`
	Province       string            `json:"province"`
	City           string            `json:"city"`
	ISP            string            `json:"isp"`
	ASN            uint              `json:"asn"`
	Organization   string            `json:"organization"`
	TimeZone       string            `json:"time_zone"`
	Latitude       float64           `json:"latitude"`
	Longitude      float64           `json:"longitude"`
	AccuracyRadius uint              `json:"accuracy_radius"`
	Location       string            `json:"location"`
	Public         bool              `json:"public"`
	Sources        map[string]string `json:"sources"`
}

type Config struct {
	Enabled   bool
	Directory string
	Sources   map[string]string
}
type Status struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updated_at"`
	Ready     bool   `json:"ready"`
	Error     string `json:"error,omitempty"`
}
type database struct {
	mmdb   *maxminddb.Reader
	xdb    *xdb.Searcher
	status Status
}
type Resolver func(context.Context, netip.Addr, *Result) error

type Manager struct {
	Config    Config
	Client    *http.Client
	mu        sync.RWMutex
	update    sync.Mutex
	databases map[string]*database
	resolvers []Resolver
	cancel    context.CancelFunc
	done      chan struct{}
}

// ManagerFrom 返回当前应用的共享实例；应用退出时由容器关闭。
func ManagerFrom(r cc.Resolver) (*Manager, error) {
	raw, err := r.Make("dawn.geoip")
	if err != nil {
		return nil, err
	}
	return raw.(*Manager), nil
}
func Resolve() *Manager {
	m, err := container.Make[*Manager]("dawn.geoip")
	if err != nil {
		panic(err)
	}
	return m
}
func Lookup(ctx context.Context, ip string) (Result, error) { return Resolve().Lookup(ctx, ip) }
func Update(ctx context.Context) error                      { return Resolve().Update(ctx) }

// New 只打开本地数据库，不自动联网；后台定时重载其他进程更新的文件。
func New(cfg Config) *Manager {
	if len(cfg.Sources) == 0 {
		cfg.Sources = DefaultSources()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Config: cfg, Client: &http.Client{Timeout: 5 * time.Minute}, databases: map[string]*database{}, cancel: cancel, done: make(chan struct{})}
	_ = m.Reload()
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.Reload()
			}
		}
	}()
	return m
}
func (m *Manager) Close() error {
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, db := range m.databases {
		if db.mmdb != nil {
			_ = db.mmdb.Close()
		}
	}
	m.databases = map[string]*database{}
	return nil
}

// Extend 在内置解析完成后补充或覆盖字段，不允许解析器保留Result指针。
func (m *Manager) Extend(resolver Resolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolvers = append(m.resolvers, resolver)
}
func (m *Manager) Status() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := []Status{}
	for _, db := range m.databases {
		rows = append(rows, db.status)
	}
	slices.SortFunc(rows, func(a, b Status) int { return strings.Compare(a.Name, b.Name) })
	return rows
}

// open 验证下载内容及数据库类型，成功后才能替换当前文件。
func open(path, name string) (*database, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	db := &database{status: Status{Name: name, Size: stat.Size(), UpdatedAt: stat.ModTime().UnixMilli(), Ready: true}}
	if strings.HasSuffix(name, ".xdb") {
		if err = xdb.VerifyFromFile(path); err != nil {
			return nil, err
		}
		header, err := xdb.LoadHeaderFromFile(path)
		if err != nil {
			return nil, err
		}
		version, err := xdb.VersionFromHeader(header)
		if err != nil {
			return nil, err
		}
		if name == "ip2region_v4.xdb" && version.Id != 4 || name == "ip2region_v6.xdb" && version.Id != 6 {
			return nil, i18n.Errorf("IP数据库类型不匹配")
		}
		content, err := xdb.LoadContentFromFile(path)
		if err != nil {
			return nil, err
		}
		db.xdb, err = xdb.NewWithBuffer(version, content)
		return db, err
	}
	db.mmdb, err = maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	if err = db.mmdb.Verify(); err == nil {
		expected := strings.TrimSuffix(strings.TrimPrefix(name, "GeoLite2-"), ".mmdb")
		if !strings.Contains(db.mmdb.Metadata.DatabaseType, expected) {
			err = i18n.Errorf("IP数据库类型不匹配")
		}
	}
	if err != nil {
		_ = db.mmdb.Close()
		return nil, err
	}
	return db, nil
}
func (m *Manager) Reload() error {
	m.update.Lock()
	defer m.update.Unlock()
	if !m.Config.Enabled {
		return nil
	}
	var failures []error
	for name := range m.Config.Sources {
		path := filepath.Join(m.Config.Directory, name)
		stat, err := os.Stat(path)
		m.mu.RLock()
		old := m.databases[name]
		unchanged := err == nil && old != nil && old.status.Ready && old.status.Size == stat.Size() && old.status.UpdatedAt == stat.ModTime().UnixMilli()
		m.mu.RUnlock()
		if unchanged {
			continue
		}
		var next *database
		if err == nil {
			next, err = open(path, name)
		}
		m.mu.Lock()
		if err == nil {
			m.databases[name] = next
			if old != nil && old.mmdb != nil {
				_ = old.mmdb.Close()
			}
		} else {
			if old == nil {
				old = &database{status: Status{Name: name}}
				m.databases[name] = old
			}
			old.status.Error = "IP数据库不可用"
			if !os.IsNotExist(err) {
				failures = append(failures, fmt.Errorf("%s: %w", name, err))
			}
		}
		m.mu.Unlock()
	}
	return errors.Join(failures...)
}
func (m *Manager) Update(ctx context.Context) error {
	m.update.Lock()
	if !m.Config.Enabled {
		m.update.Unlock()
		return nil
	}
	if err := os.MkdirAll(m.Config.Directory, 0750); err != nil {
		m.update.Unlock()
		return err
	}
	var failures []error
	for name, url := range m.Config.Sources {
		if filepath.Base(name) != name {
			failures = append(failures, i18n.Errorf("IP数据库名称无效"))
			continue
		}
		if err := m.download(ctx, name, url); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}
	m.update.Unlock()
	failures = append(failures, m.Reload())
	return errors.Join(failures...)
}
func (m *Manager) download(ctx context.Context, name, url string) error {
	target := filepath.Join(m.Config.Directory, name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if stat, err := os.Stat(target); err == nil {
		request.Header.Set("If-Modified-Since", stat.ModTime().UTC().Format(http.TimeFormat))
	}
	response, err := m.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return i18n.Errorf("IP数据库下载失败: HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(m.Config.Directory, ".geoip-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	size, err := io.Copy(file, io.LimitReader(response.Body, (128<<20)+1))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if size > 128<<20 {
		return i18n.Errorf("IP数据库超过128MB")
	}
	db, err := open(file.Name(), name)
	if err != nil {
		return err
	}
	if db.mmdb != nil {
		_ = db.mmdb.Close()
	}
	if modified, err := http.ParseTime(response.Header.Get("Last-Modified")); err == nil {
		if err := os.Chtimes(file.Name(), time.Now(), modified); err != nil {
			return err
		}
	}
	return os.Rename(file.Name(), target)
}

var specialPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}

func (m *Manager) Lookup(ctx context.Context, ip string) (Result, error) {
	result := Result{IP: ip, Sources: map[string]string{}, Location: "未知区域"}
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return result, i18n.Errorf("无效的IP地址")
	}
	address = address.Unmap()
	result.IP = address.String()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || slices.ContainsFunc(specialPrefixes, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
		result.Location = "内网或保留地址"
		return result, nil
	}
	result.Public = true
	if !m.Config.Enabled {
		return result, nil
	}
	m.mu.RLock()
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
		Location     struct {
			TimeZone       string  `maxminddb:"time_zone"`
			Latitude       float64 `maxminddb:"latitude"`
			Longitude      float64 `maxminddb:"longitude"`
			AccuracyRadius uint    `maxminddb:"accuracy_radius"`
		} `maxminddb:"location"`
	}
	if db := m.databases["GeoLite2-City.mmdb"]; db != nil && db.mmdb != nil {
		err = db.mmdb.Lookup(address).Decode(&record)
		if err == nil && record.Country.ISO != "" {
			result.Sources["region"] = "geolite2_city"
		}
	}
	if record.Country.ISO == "" {
		if db := m.databases["GeoLite2-Country.mmdb"]; db != nil && db.mmdb != nil {
			err = db.mmdb.Lookup(address).Decode(&record)
			if err == nil && record.Country.ISO != "" {
				result.Sources["region"] = "geolite2_country"
			}
		}
	}
	result.CountryCode = record.Country.ISO
	result.Country = cmp.Or(record.Country.Names["zh-CN"], record.Country.Names["en"])
	result.City = cmp.Or(record.City.Names["zh-CN"], record.City.Names["en"])
	if len(record.Subdivisions) > 0 {
		result.Province = cmp.Or(record.Subdivisions[0].Names["zh-CN"], record.Subdivisions[0].Names["en"])
	}
	result.TimeZone = record.Location.TimeZone
	result.Latitude = record.Location.Latitude
	result.Longitude = record.Location.Longitude
	result.AccuracyRadius = record.Location.AccuracyRadius
	name := "ip2region_v6.xdb"
	if address.Is4() {
		name = "ip2region_v4.xdb"
	}
	if db := m.databases[name]; db != nil && db.xdb != nil {
		// 全内存模式复制Searcher以隔离SDK每次查询写入的IO计数，共享只读数据库缓冲区。
		searcher := *db.xdb
		region, e := searcher.Search(address.String())
		if e == nil {
			parts := strings.Split(region, "|")
			if len(parts) == 5 {
				for i := range parts {
					if parts[i] == "0" {
						parts[i] = ""
					}
				}
				if parts[4] == "CN" || result.CountryCode == "" {
					result.Country = parts[0]
					result.CountryCode = parts[4]
					result.Province = parts[1]
					result.City = parts[2]
					result.ISP = parts[3]
					result.Sources["region"] = "ip2region"
					if parts[3] != "" {
						result.Sources["isp"] = "ip2region"
					}
				}
			}
		}
	}
	if db := m.databases["GeoLite2-ASN.mmdb"]; db != nil && db.mmdb != nil {
		var asn struct {
			Number       uint   `maxminddb:"autonomous_system_number"`
			Organization string `maxminddb:"autonomous_system_organization"`
		}
		if db.mmdb.Lookup(address).Decode(&asn) == nil {
			result.ASN = asn.Number
			result.Organization = asn.Organization
			if asn.Number != 0 {
				result.Sources["asn"] = "geolite2_asn"
			}
		}
	}
	resolvers := slices.Clone(m.resolvers)
	m.mu.RUnlock()
	for _, resolver := range resolvers {
		if err := resolver(ctx, address, &result); err != nil {
			return result, err
		}
	}
	parts := []string{result.Country, result.Province, result.City}
	parts = slices.DeleteFunc(parts, func(s string) bool { return s == "" })
	if len(parts) > 0 {
		result.Location = strings.Join(parts, " ")
	}
	return result, nil
}
