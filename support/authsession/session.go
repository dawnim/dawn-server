// Package authsession为Bearer认证复用PrismGo Session存储、编码、加密和锁。
package authsession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/constants"
	redisfacade "github.com/prismgo/framework/redis"
	"github.com/prismgo/framework/session"
	"github.com/redis/go-redis/v9"
)

var ErrUnauthenticated = errors.New("登录已失效")

// Record仅保存身份和设备信息，账号状态和权限仍从业务层读取。
type Record struct {
	UserType         string `json:"user_type,omitempty"` // 签发时的用户类型，防止旧游客凭证随账号升级
	ID               string `json:"id"`
	UserID           uint   `json:"user_id"`
	AppID            uint   `json:"app_id,omitempty"`
	Verified         bool   `json:"verified,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
	RefreshExpiresAt int64  `json:"-"`
	CreatedAt        int64  `json:"created_at"`
	IP               string `json:"ip,omitempty"`
	UserAgent        string `json:"user_agent,omitempty"`
	AccessHash       string `json:"-"`
	RefreshHash      string `json:"-"`
}

// Store按admin/user隔离凭证，索引用于设备列表和撤销全部设备。
type Store struct {
	driver *session.RedisDriver
	client redis.UniversalClient
	prefix string
	config session.Config
}

func New(scope string) (*Store, error) {
	cfg, err := session.ConfigFromFacadeStrict()
	if err != nil {
		return nil, err
	}
	if cfg.Driver != "redis" {
		return nil, errors.New("登录会话需要配置SESSION_DRIVER=redis")
	}
	cfg.Redis.Prefix += ":" + constants.AuthSessionNamespace + ":" + scope
	driver, err := session.NewRedisDriver(cfg)
	if err != nil {
		return nil, err
	}
	client, err := redisfacade.Client(cfg.Redis.Connection)
	if err != nil {
		return nil, err
	}
	return &Store{driver: driver, client: client, prefix: cfg.Redis.Prefix, config: cfg}, nil
}

// Create只保存凭证摘要；访客不需要独立刷新凭证。
func (s *Store) Create(ctx context.Context, r Record, accessTTL, refreshTTL time.Duration) (Record, string, string, error) {
	r.ID = rand.Text() + rand.Text()
	r.CreatedAt = time.Now().UnixMilli()
	r.ExpiresAt = time.Now().Add(accessTTL).UnixMilli()
	var refresh string
	if refreshTTL > 0 {
		r.RefreshExpiresAt = time.Now().Add(refreshTTL).UnixMilli()
		refresh = r.ID + "." + rand.Text()
		r.RefreshHash = Hash(refresh)
	}
	token := r.ID + "." + rand.Text()
	r.AccessHash = Hash(token)
	if err := s.write(ctx, r); err != nil {
		return Record{}, "", "", err
	}
	expires := max(r.ExpiresAt, r.RefreshExpiresAt)
	_, err := s.client.Eval(ctx, `for _,key in ipairs(KEYS) do redis.call('ZREMRANGEBYSCORE',key,'-inf',ARGV[1]);redis.call('ZADD',key,ARGV[2],ARGV[3]);if redis.call('PTTL',key)<tonumber(ARGV[2])-tonumber(ARGV[1]) then redis.call('PEXPIREAT',key,ARGV[2]) end end;return 1`, []string{s.prefix + ":index", s.prefix + ":user:" + strconv.FormatUint(uint64(r.UserID), 10)}, r.CreatedAt, expires, r.ID).Result()
	if err != nil {
		_ = s.driver.Destroy(context.WithoutCancel(ctx), r.ID)
	}
	return r, token, refresh, err
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) write(ctx context.Context, r Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	expires := time.UnixMilli(max(r.ExpiresAt, r.RefreshExpiresAt))
	return s.driver.Write(ctx, r.ID, session.Payload{ID: r.ID, CreatedAt: time.UnixMilli(r.CreatedAt), LastActivity: time.Now(), Values: map[string]any{"record": string(raw), "access": r.AccessHash, "refresh": r.RefreshHash, "refresh_expires_at": strconv.FormatInt(r.RefreshExpiresAt, 10)}}, &expires)
}

// Read同时检查可撤销索引，孤立payload不能恢复已注销的登录。
func (s *Store) Read(ctx context.Context, id string) (Record, error) {
	var r Record
	p, err := s.driver.Read(ctx, id)
	if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrSessionExpired) || errors.Is(err, session.ErrInvalidSessionID) {
		return r, ErrUnauthenticated
	}
	if err != nil {
		return r, err
	}
	raw, _ := p.Values["record"].(string)
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	r.AccessHash, _ = p.Values["access"].(string)
	r.RefreshHash, _ = p.Values["refresh"].(string)
	refreshExpires, _ := p.Values["refresh_expires_at"].(string)
	r.RefreshExpiresAt, _ = strconv.ParseInt(refreshExpires, 10, 64)
	score, err := s.client.ZScore(ctx, s.prefix+":user:"+strconv.FormatUint(uint64(r.UserID), 10), id).Result()
	if errors.Is(err, redis.Nil) || (err == nil && score <= float64(time.Now().UnixMilli())) {
		return Record{}, ErrUnauthenticated
	}
	return r, err
}

func (s *Store) Authenticate(ctx context.Context, token string, refresh bool) (Record, error) {
	id, _, ok := strings.Cut(token, ".")
	if !ok {
		return Record{}, ErrUnauthenticated
	}
	r, err := s.Read(ctx, id)
	if err != nil {
		return r, err
	}
	hash, expires := r.AccessHash, r.ExpiresAt
	if refresh {
		hash, expires = r.RefreshHash, r.RefreshExpiresAt
	}
	if expires <= time.Now().UnixMilli() || subtle.ConstantTimeCompare([]byte(hash), []byte(Hash(token))) != 1 {
		return Record{}, ErrUnauthenticated
	}
	return r, nil
}

// Refresh使用框架锁串行轮转，同一个刷新凭证只能成功一次。
func (s *Store) Refresh(ctx context.Context, token string, accessTTL, refreshTTL time.Duration) (string, string, error) {
	id, _, ok := strings.Cut(token, ".")
	if !ok {
		return "", "", ErrUnauthenticated
	}
	lock, err := s.driver.Lock(ctx, id, s.config.Lock.TTL, s.config.Lock.Wait)
	if errors.Is(err, session.ErrInvalidSessionID) {
		return "", "", ErrUnauthenticated
	}
	if err != nil {
		return "", "", err
	}
	defer lock.Release(context.WithoutCancel(ctx))
	r, err := s.Authenticate(ctx, token, true)
	if err != nil {
		return "", "", err
	}
	access, refresh := id+"."+rand.Text(), id+"."+rand.Text()
	r.AccessHash, r.RefreshHash = Hash(access), Hash(refresh)
	r.ExpiresAt = time.Now().Add(accessTTL).UnixMilli()
	r.RefreshExpiresAt = time.Now().Add(refreshTTL).UnixMilli()
	if err = s.write(ctx, r); err != nil {
		return "", "", err
	}
	// XX禁止刷新与注销并发时重新创建已撤销的索引。
	updated, err := s.client.Eval(ctx, `if not redis.call('ZSCORE',KEYS[2],ARGV[2]) then return 0 end;for _,key in ipairs(KEYS) do redis.call('ZADD',key,'XX',ARGV[1],ARGV[2]);if redis.call('PTTL',key)<tonumber(ARGV[1])-tonumber(ARGV[3]) then redis.call('PEXPIREAT',key,ARGV[1]) end end;return 1`, []string{s.prefix + ":index", s.prefix + ":user:" + strconv.FormatUint(uint64(r.UserID), 10)}, max(r.ExpiresAt, r.RefreshExpiresAt), id, time.Now().UnixMilli()).Int()
	if err != nil {
		return "", "", err
	}
	if updated == 0 {
		return "", "", ErrUnauthenticated
	}
	return access, refresh, nil
}

func (s *Store) Revoke(ctx context.Context, id string) error {
	r, err := s.Read(ctx, id)
	if errors.Is(err, ErrUnauthenticated) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.client.ZRem(ctx, s.prefix+":user:"+strconv.FormatUint(uint64(r.UserID), 10), id).Err(); err != nil {
		return err
	}
	if err = s.client.ZRem(ctx, s.prefix+":index", id).Err(); err != nil {
		return err
	}
	return s.driver.Destroy(ctx, id)
}

// RevokeUsers先删除有效索引，刷新中途写回payload也不能恢复登录。
func (s *Store) RevokeUsers(ctx context.Context, ids ...uint) error {
	for _, id := range ids {
		key := s.prefix + ":user:" + strconv.FormatUint(uint64(id), 10)
		rows, err := s.client.Eval(ctx, `local ids=redis.call('ZRANGE',KEYS[1],0,-1);redis.call('DEL',KEYS[1]);return ids`, []string{key}).StringSlice()
		if err != nil {
			return err
		}
		for _, sessionID := range rows {
			if err = s.client.ZRem(ctx, s.prefix+":index", sessionID).Err(); err != nil {
				return err
			}
			if err = s.driver.Destroy(ctx, sessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

// List使用Redis游标读取设备，不伪造总数或按页截断扫描结果。
func (s *Store) List(ctx context.Context, userID uint, cursor uint64, count int64) ([]Record, uint64, error) {
	key := s.prefix + ":index"
	if userID != 0 {
		key = s.prefix + ":user:" + strconv.FormatUint(uint64(userID), 10)
	}
	if err := s.client.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(time.Now().UnixMilli(), 10)).Err(); err != nil {
		return nil, 0, err
	}
	ids, next, err := s.client.ZScan(ctx, key, cursor, "*", count).Result()
	if err != nil {
		return nil, 0, err
	}
	rows := []Record{}
	for i := 0; i < len(ids); i += 2 {
		r, err := s.Read(ctx, ids[i])
		if errors.Is(err, ErrUnauthenticated) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if r.ExpiresAt > time.Now().UnixMilli() {
			rows = append(rows, r)
		}
	}
	return rows, next, nil
}
