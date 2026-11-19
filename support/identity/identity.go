// Package identity 提供安装时固定的可逆用户编号，不作为认证凭证。
package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/capitalone/fpe/ff1"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/settings"
	"github.com/prismgo/framework/container"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// SettingKey 是仅供服务端读取的安装配置组，不提供后台编辑。
	SettingKey     = "__user_identity"
	alphabet       = "012356789"
	positiveDigits = "13468" // 九进制字符映射后为1、3、5、7、9。
	negativeDigits = "0257"  // 九进制字符映射后为0、2、6、8。
)

// 每档为正负数各预留32×9^(位数-2)个值，末档覆盖剩余int64范围。
var tierEnds = [...]uint64{
	1_889_568, 18_895_680, 171_950_688, 1_549_445_760,
	13_946_901_408, 125_524_002_240, 1_129_717_909_728,
	10_167_463_077_120, 91_507_169_583_648, 823_564_528_142_400,
	7_412_080_755_171_168, 66_708_726_798_430_080,
	600_378_541_187_760_288, 5_403_406_870_691_732_160, 1 << 63,
}

// Codec的密钥属于安装配置，创建用户后不得改变。
type Codec struct {
	Key string `json:"key"`
}

// Install 只在首次安装生成编号密钥，已有配置永不覆盖。
func Install(ctx context.Context, db *gorm.DB) error {
	if _, err := load(ctx, db); err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var users int64
	if err := db.WithContext(ctx).Table("users").Count(&users).Error; err != nil {
		return err
	}
	if users > 0 {
		return i18n.Errorf("用户编号配置缺失，请恢复数据库备份")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	value, _ := json.Marshal(Codec{Key: hex.EncodeToString(key)})
	return db.WithContext(ctx).Table("admin_settings").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"key"})}).Create(map[string]any{"key": SettingKey, "name": "系统:用户编号密钥，禁止修改或删除", "value": string(value), "revision": 1, "created_at": int64(0), "updated_at": int64(0)}).Error
}

// Load 复用配置分组缓存，未命中才查询固定配置，绝不生成新密钥。
func Load(ctx context.Context, db *gorm.DB) (*Codec, error) {
	store, err := container.Make[*settings.Store]("dawn.settings")
	if err != nil {
		return nil, err
	}
	value, err := store.Read(ctx, db, SettingKey, "key")
	if err != nil {
		return nil, err
	}
	var c Codec
	if err := json.Unmarshal(value, &c.Key); err != nil {
		return nil, err
	}
	if _, err := c.cipher(); err != nil {
		return nil, err
	}
	return &c, nil
}

// load直接读取安装记录，安装校验不能依赖缓存中的旧值。
func load(ctx context.Context, db *gorm.DB) (Codec, error) {
	var row struct{ Value string }
	if err := db.Session(&gorm.Session{NewDB: true, Context: ctx}).Table("admin_settings").Where(map[string]any{"key": SettingKey}).Take(&row).Error; err != nil {
		return Codec{}, err
	}
	var c Codec
	if err := json.Unmarshal([]byte(row.Value), &c); err != nil {
		return Codec{}, err
	}
	if _, err := c.cipher(); err != nil {
		return Codec{}, err
	}
	return c, nil
}

// cipher每次创建独立实例，FF1内部的CBC状态不能跨goroutine共享。
func (c *Codec) cipher() (ff1.Cipher, error) {
	key, err := hex.DecodeString(c.Key)
	if err != nil || len(key) != 32 {
		return ff1.Cipher{}, i18n.Errorf("用户编号配置无效")
	}
	return ff1.NewCipher(9, 0, key, nil)
}

// ID2Big 可逆转换非零int64；7位起按区间升位，无前导0、不含4，中间位奇正偶负。
func (c *Codec) ID2Big(id int64) (string, error) {
	if id == 0 {
		return "", i18n.Errorf("用户编号超出范围")
	}
	magnitude, digits := uint64(id), positiveDigits
	if id < 0 {
		magnitude, digits = uint64(-(id+1))+1, negativeDigits
	}
	tier := sort.Search(len(tierEnds), func(i int) bool { return magnitude <= tierEnds[i] })
	rank := magnitude - 1
	if tier > 0 {
		rank -= tierEnds[tier-1]
	}
	width, factor := tier+7, uint64(8*len(digits))
	body := strconv.FormatUint(rank/factor, 9)
	body = strings.Repeat("0", width-2-len(body)) + body
	middle := width / 2
	value := string(byte(rank%8)+'1') + body[:middle-1] + string(digits[rank/8%uint64(len(digits))]) + body[middle-1:]
	value, err := c.permute(value, digits, false)
	if err != nil {
		return "", err
	}
	result := []byte(value)
	for i, digit := range result {
		result[i] = alphabet[digit-'0']
	}
	return string(result), nil
}

// Big2ID 还原非零int64，拒绝非法或超出档位的编号；编号不能作为认证凭证。
func (c *Codec) Big2ID(value string) (int64, error) {
	if len(value) < 7 || len(value) > len(tierEnds)+6 || value[0] == '0' || strings.Trim(value, alphabet) != "" {
		return 0, i18n.Errorf("无效的用户编号")
	}
	middle, digits := len(value)/2, positiveDigits
	negative := value[middle]%2 == 0
	if negative {
		digits = negativeDigits
	}
	input := []byte(value)
	for i, digit := range input {
		input[i] = byte(strings.IndexByte(alphabet, digit)) + '0'
	}
	value, err := c.permute(string(input), digits, true)
	if err != nil {
		return 0, err
	}
	tier, lower := len(value)-7, uint64(0)
	if tier > 0 {
		lower = tierEnds[tier-1]
	}
	capacity, factor := tierEnds[tier]-lower, uint64(8*len(digits))
	body, err := strconv.ParseUint(value[1:middle]+value[middle+1:], 9, 64)
	if err != nil || body > (capacity-1)/factor {
		return 0, i18n.Errorf("无效的用户编号")
	}
	rank := body*factor + uint64(strings.IndexByte(digits, value[middle]))*8 + uint64(value[0]-'1')
	if rank >= capacity {
		return 0, i18n.Errorf("无效的用户编号")
	}
	magnitude := lower + rank + 1
	if negative {
		return -int64(magnitude-1) - 1, nil
	}
	if magnitude > math.MaxInt64 {
		return 0, i18n.Errorf("无效的用户编号")
	}
	return int64(magnitude), nil
}

// permute用cycle walking保留首位与符号位约束；正反转换使用同一个受限域。
func (c *Codec) permute(value, digits string, decrypt bool) (string, error) {
	cipher, err := c.cipher()
	if err != nil {
		return "", err
	}
	convert := cipher.Encrypt
	if decrypt {
		convert = cipher.Decrypt
	}
	for {
		value, err = convert(value)
		if err != nil || value[0] != '0' && strings.ContainsRune(digits, rune(value[len(value)/2])) {
			return value, err
		}
	}
}
