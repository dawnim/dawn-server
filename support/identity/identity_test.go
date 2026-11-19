package identity

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCodec(t *testing.T) {
	c := Codec{Key: strings.Repeat("ab", 32)}
	seen := map[string]int64{}
	check := func(id int64, width int) {
		t.Helper()
		value, err := c.ID2Big(id)
		if err != nil || len(value) != width || value[0] == '0' || strings.Trim(value, "012356789") != "" {
			t.Fatalf("%d => %q, want width %d: %v", id, value, width, err)
		}
		if (value[len(value)/2]%2 == 0) != (id < 0) {
			t.Fatalf("wrong sign: %d => %s", id, value)
		}
		if previous, ok := seen[value]; ok && previous != id {
			t.Fatalf("collision: %d and %d => %s", previous, id, value)
		}
		seen[value] = id
		decoded, err := c.Big2ID(value)
		if err != nil || decoded != id {
			t.Fatalf("round trip %d => %s => %d: %v", id, value, decoded, err)
		}
		repeated, err := c.ID2Big(id)
		if err != nil || repeated != value {
			t.Fatalf("unstable encoding: %d => %s, %s: %v", id, value, repeated, err)
		}
	}
	for id := int64(1); id <= 1000; id++ {
		check(id, 7)
		check(-id, 7)
	}
	// 独立按容量公式计算连续区间，检查所有档位的起止值及相邻值。
	end, capacity := uint64(0), uint64(32*9*9*9*9*9)
	for width := 7; width <= 20; width++ {
		start := end + 1
		end += capacity
		if tierEnds[width-7] != end {
			t.Fatalf("tier %d capacity: %d, want %d", width, tierEnds[width-7], end)
		}
		for _, id := range []int64{int64(start), int64(start + 1), int64(end - 1), int64(end)} {
			check(id, width)
			check(-id, width)
		}
		if width < 20 {
			capacity *= 9
		}
	}
	for _, id := range []int64{int64(end) + 1, math.MaxInt64} {
		check(id, 21)
		check(-id, 21)
	}
	check(math.MinInt64, 21)
	random := rand.New(rand.NewPCG(1, 2))
	for range 100 {
		id := random.Int64() + 1
		width := 7
		for uint64(id) > tierEnds[width-7] {
			width++
		}
		check(id, width)
		check(-id, width)
	}
	if _, err := c.ID2Big(0); err == nil {
		t.Fatal("zero accepted")
	}
	for _, invalid := range []string{"", "123", "123567", "0123567", "1234567", "abcdefg", "-123567", strings.Repeat("9", 22), "１２３５６７８"} {
		if _, err := c.Big2ID(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	// 正数域有额外编码空间，最后一档也不能解出超出int64的数值。
	for _, seed := range []string{"8886888", "888888888868888888888"} {
		value, err := c.permute(seed, positiveDigits, false)
		if err != nil {
			t.Fatal(err)
		}
		output := []byte(value)
		for i, digit := range output {
			output[i] = "012356789"[digit-'0']
		}
		if _, err := c.Big2ID(string(output)); err == nil {
			t.Fatalf("out-of-tier value accepted: %s", output)
		}
	}
	upper := Codec{Key: strings.ToUpper(c.Key)}
	value, _ := c.ID2Big(1)
	if other, err := upper.ID2Big(1); err != nil || other != value {
		t.Fatalf("hex case changed ID: %s, %s: %v", value, other, err)
	}
	for _, key := range []string{"", "invalid", strings.Repeat("ab", 16)} {
		invalid := Codec{Key: key}
		if _, err := invalid.ID2Big(1); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
}

// 固定样例防止正反实现同时变化后，旧编号无法还原却仍通过往返测试。
func TestCodecVectors(t *testing.T) {
	c := Codec{Key: strings.Repeat("ab", 32)}
	for _, tc := range []struct {
		id    int64
		value string
	}{
		{1, "7623023"}, {-1, "6882695"},
		{1_889_568, "1531032"}, {1_889_569, "38317700"},
		{18_895_680, "76771328"}, {18_895_681, "933015897"},
		{171_950_688, "996876683"}, {171_950_689, "8319355653"},
		{math.MaxInt64, "722680015890871796876"},
		{math.MinInt64, "693997935228112230583"},
	} {
		if value, err := c.ID2Big(tc.id); err != nil || value != tc.value {
			t.Fatalf("fixed encoding %d: %s, %v", tc.id, value, err)
		}
		if id, err := c.Big2ID(tc.value); err != nil || id != tc.id {
			t.Fatalf("fixed decoding %s: %d, %v", tc.value, id, err)
		}
	}
}

func TestCodecConcurrent(t *testing.T) {
	c := Codec{Key: strings.Repeat("cd", 32)}
	for range 8 {
		t.Run("shared", func(t *testing.T) {
			t.Parallel()
			for _, id := range []int64{1, -1, 1_889_568, -1_889_569, math.MaxInt64, math.MinInt64} {
				value, err := c.ID2Big(id)
				if err != nil {
					t.Fatal(err)
				}
				if decoded, err := c.Big2ID(value); err != nil || decoded != id {
					t.Fatalf("concurrent %d => %s => %d: %v", id, value, decoded, err)
				}
			}
		})
	}
}

func BenchmarkCodec(b *testing.B) {
	c := Codec{Key: strings.Repeat("ab", 32)}
	for name, id := range map[string]int64{"positive_short": 1, "negative_short": -1, "positive_10": 999_999_999, "negative_21": math.MinInt64} {
		value, err := c.ID2Big(id)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name+"/encode", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := c.ID2Big(id); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/decode", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := c.Big2ID(value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestInstallIsPermanent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.Exec("CREATE TABLE admin_settings (key TEXT PRIMARY KEY, name TEXT, value TEXT, revision INTEGER, created_at INTEGER, updated_at INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Install(ctx, db); err != nil {
		t.Fatal(err)
	}
	first, err := load(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if err := Install(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	second, err := load(ctx, db)
	if err != nil || first != second {
		t.Fatalf("configuration changed: %v", err)
	}
	value, err := first.ID2Big(1)
	if err != nil || len(value) != 7 || value[0] == '0' {
		t.Fatalf("public ID %q: %v", value, err)
	}
	if id, err := second.Big2ID(value); err != nil || id != 1 {
		t.Fatalf("reloaded configuration cannot decode: %d: %v", id, err)
	}
	_, err = first.Big2ID("bad")
	if got := i18n.Message(err, "zh"); got != "无效的用户编号" {
		t.Fatalf("Chinese: %q", got)
	}
	if err := db.Exec("DELETE FROM admin_settings").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := load(ctx, db); err == nil {
		t.Fatal("missing configuration regenerated")
	}
	if err := db.Exec("INSERT INTO users (id) VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := Install(ctx, db); err == nil {
		t.Fatal("installation replaced missing configuration for existing users")
	}
}
