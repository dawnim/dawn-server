// Package clientip resolves client addresses behind configurable proxies.
package clientip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var DefaultHeaders = []string{"CF-Connecting-IP", "True-Client-IP", "Ali-CDN-Real-IP", "X-Forwarded-For", "X-Real-IP", "Client-IP", "X-Cluster-Client-IP", "Forwarded-For", "X-Forwarded", "Forwarded"}

// Config controls client address resolution and optional CDN range updates.
type Config struct {
	Mode           string
	Headers        []string
	TrustedProxies []string
	Cloudflare     bool
	CloudFront     bool
	Directory      string
}

type ranges struct {
	proxies    []netip.Prefix
	cloudflare []netip.Prefix
	cloudfront []netip.Prefix
}

type Status struct {
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
	Count     int       `json:"count"`
}

// Resolver maintains immutable snapshots for concurrent requests.
type Resolver struct {
	config   Config
	snapshot atomic.Pointer[ranges]
	mu       sync.RWMutex
	updateMu sync.Mutex
	status   map[string]Status
	cancel   context.CancelFunc
	done     chan struct{}
	client   *http.Client
}

func New(cfg Config) (*Resolver, error) {
	if cfg.Mode == "" {
		cfg.Mode = "trusted"
	}
	if cfg.Mode != "trusted" && cfg.Mode != "simple" {
		return nil, fmt.Errorf("invalid client IP mode %q", cfg.Mode)
	}
	if len(cfg.Headers) == 0 {
		cfg.Headers = DefaultHeaders
	}
	proxies, err := parsePrefixes(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	r := &Resolver{config: cfg, status: map[string]Status{}, client: &http.Client{Timeout: 15 * time.Second}, done: make(chan struct{})}
	initial := &ranges{proxies: proxies}
	for _, name := range []string{"cloudflare", "cloudfront"} {
		if cfg.Directory == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cfg.Directory, name+".json"))
		if err != nil {
			continue
		}
		var cached struct {
			Prefixes  []string  `json:"prefixes"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if json.Unmarshal(data, &cached) != nil {
			continue
		}
		parsed, err := parsePrefixes(cached.Prefixes)
		if err != nil || len(parsed) == 0 {
			continue
		}
		if name == "cloudflare" {
			initial.cloudflare = parsed
		} else {
			initial.cloudfront = parsed
		}
		r.status[name] = Status{UpdatedAt: cached.UpdatedAt, Count: len(parsed)}
	}
	r.snapshot.Store(initial)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		defer close(r.done)
		if cfg.Cloudflare || cfg.CloudFront {
			r.Update(ctx)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Update(ctx)
			}
		}
	}()
	return r, nil
}

func (r *Resolver) Close() error { r.cancel(); <-r.done; return nil }

func parsePrefixes(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			ip, e := netip.ParseAddr(value)
			if e != nil {
				return nil, fmt.Errorf("invalid proxy address %q", value)
			}
			prefix = netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen())
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func address(value string) (netip.Addr, bool) {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	ip, err := netip.ParseAddr(value)
	return ip.Unmap(), err == nil
}

func headerAddresses(name string, values []string) []netip.Addr {
	var result []netip.Addr
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(name, "Forwarded") {
				element := item
				item = ""
				for _, part := range strings.Split(element, ";") {
					key, v, ok := strings.Cut(strings.TrimSpace(part), "=")
					if ok && strings.EqualFold(key, "for") {
						item = v
						break
					}
				}
			}
			if ip, ok := address(item); ok {
				result = append(result, ip)
			}
		}
	}
	return result
}

// IP resolves a request using the configured mode.
func (r *Resolver) IP(req *http.Request) string {
	remote, ok := address(req.RemoteAddr)
	if !ok {
		return ""
	}
	if r.config.Mode == "simple" {
		fallback := netip.Addr{}
		for _, name := range r.config.Headers {
			name = strings.TrimSpace(name)
			for _, ip := range headerAddresses(name, req.Header.Values(name)) {
				if !fallback.IsValid() {
					fallback = ip
				}
				if ip.IsGlobalUnicast() && !ip.IsPrivate() {
					return ip.String()
				}
			}
		}
		if remote.IsGlobalUnicast() && !remote.IsPrivate() {
			return remote.String()
		}
		if fallback.IsValid() {
			return fallback.String()
		}
		return remote.String()
	}
	snapshot := r.snapshot.Load()
	trusted := func(ip netip.Addr) bool {
		return contains(snapshot.proxies, ip) || r.config.Cloudflare && contains(snapshot.cloudflare, ip) || r.config.CloudFront && contains(snapshot.cloudfront, ip)
	}
	if !trusted(remote) {
		return remote.String()
	}
	for _, name := range r.config.Headers {
		name = strings.TrimSpace(name)
		values := req.Header.Values(name)
		if len(values) == 0 {
			continue
		}
		current := remote
		parts := strings.Split(strings.Join(values, ","), ",")
		for i := len(parts) - 1; i >= 0 && trusted(current); i-- {
			item := strings.TrimSpace(parts[i])
			if strings.EqualFold(name, "Forwarded") {
				item = ""
				for _, part := range strings.Split(parts[i], ";") {
					key, value, found := strings.Cut(strings.TrimSpace(part), "=")
					if found && strings.EqualFold(key, "for") {
						item = value
						break
					}
				}
			}
			next, valid := address(item)
			if !valid {
				return current.String()
			}
			current = next
		}
		return current.String()
	}
	return remote.String()
}

func (r *Resolver) Status() map[string]Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Status, len(r.status))
	for k, v := range r.status {
		result[k] = v
	}
	return result
}

func (r *Resolver) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err == nil && len(data) > 10<<20 {
		return nil, fmt.Errorf("range list exceeds size limit")
	}
	return data, err
}

// Update fetches enabled providers, retaining the previous snapshot on failure.
func (r *Resolver) Update(ctx context.Context) {
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	for _, name := range []string{"cloudflare", "cloudfront"} {
		if name == "cloudflare" && !r.config.Cloudflare || name == "cloudfront" && !r.config.CloudFront {
			continue
		}
		var values []string
		var err error
		if name == "cloudflare" {
			for _, url := range []string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"} {
				var data []byte
				data, err = r.fetch(ctx, url)
				if err != nil {
					break
				}
				lines := strings.Fields(string(data))
				if len(lines) == 0 {
					err = fmt.Errorf("empty range list")
					break
				}
				for _, line := range lines {
					prefix, parseErr := netip.ParsePrefix(line)
					if parseErr != nil || prefix.Addr().Is4() != strings.HasSuffix(url, "v4") {
						err = fmt.Errorf("invalid range list from %s", url)
						break
					}
				}
				if err != nil {
					break
				}
				values = append(values, lines...)
			}
		} else {
			var data []byte
			data, err = r.fetch(ctx, "https://ip-ranges.amazonaws.com/ip-ranges.json")
			if err == nil {
				var feed struct {
					Prefixes []struct {
						Prefix  string `json:"ip_prefix"`
						Service string `json:"service"`
					} `json:"prefixes"`
					IPv6 []struct {
						Prefix  string `json:"ipv6_prefix"`
						Service string `json:"service"`
					} `json:"ipv6_prefixes"`
				}
				err = json.Unmarshal(data, &feed)
				v4Count, v6Count := 0, 0
				for _, p := range feed.Prefixes {
					if p.Service == "CLOUDFRONT_ORIGIN_FACING" {
						prefix, parseErr := netip.ParsePrefix(p.Prefix)
						if parseErr != nil || !prefix.Addr().Is4() {
							err = fmt.Errorf("invalid CloudFront IPv4 range %q", p.Prefix)
							break
						}
						values = append(values, p.Prefix)
						v4Count++
					}
				}
				for _, p := range feed.IPv6 {
					if p.Service == "CLOUDFRONT_ORIGIN_FACING" {
						prefix, parseErr := netip.ParsePrefix(p.Prefix)
						if parseErr != nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
							err = fmt.Errorf("invalid CloudFront IPv6 range %q", p.Prefix)
							break
						}
						values = append(values, p.Prefix)
						v6Count++
					}
				}
				if (v4Count == 0 || v6Count == 0) && err == nil {
					err = fmt.Errorf("empty range list")
				}
			}
		}
		var parsed []netip.Prefix
		if err == nil {
			parsed, err = parsePrefixes(values)
		}
		if err == nil && r.config.Directory != "" {
			err = os.MkdirAll(r.config.Directory, 0755)
			if err == nil {
				data, _ := json.Marshal(struct {
					Prefixes  []string  `json:"prefixes"`
					UpdatedAt time.Time `json:"updated_at"`
				}{values, time.Now().UTC()})
				var f *os.File
				f, err = os.CreateTemp(r.config.Directory, ".ranges-*")
				if err == nil {
					tempName := f.Name()
					defer os.Remove(tempName)
					if _, err = f.Write(data); err == nil {
						err = f.Sync()
					}
					if closeErr := f.Close(); err == nil {
						err = closeErr
					}
					if err == nil {
						err = os.Rename(tempName, filepath.Join(r.config.Directory, name+".json"))
					}
				}
			}
		}
		r.mu.Lock()
		status := r.status[name]
		if err != nil {
			status.Error = err.Error()
			if ctx.Err() == nil {
				log.Printf("clientip: update %s ranges: %v", name, err)
			}
		} else {
			status = Status{UpdatedAt: time.Now().UTC(), Count: len(parsed)}
			old := r.snapshot.Load()
			next := &ranges{proxies: old.proxies, cloudflare: old.cloudflare, cloudfront: old.cloudfront}
			if name == "cloudflare" {
				next.cloudflare = parsed
			} else {
				next.cloudfront = parsed
			}
			r.snapshot.Store(next)
		}
		r.status[name] = status
		r.mu.Unlock()
	}
}
