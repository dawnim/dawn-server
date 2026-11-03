package i18n

import (
	"encoding/json"
	"regexp"
	"slices"
	"testing"
)

func TestCatalogs(t *testing.T) {
	data, err := files.ReadFile("lang/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var lines map[string]string
	if err := json.Unmarshal(data, &lines); err != nil {
		t.Fatal(err)
	}
	verbs := regexp.MustCompile(`%[-+#0-9.]*[a-zA-Z]`)
	ending := regexp.MustCompile(`[。！？，、；：!?.,;:]$`)
	for key, target := range lines {
		if target == "" {
			t.Errorf("缺少英文: %s", key)
		}
		if ending.MatchString(key) {
			t.Errorf("中文文案末尾不加标点: %s", key)
		}
		if !slices.Equal(verbs.FindAllString(key, -1), verbs.FindAllString(target, -1)) {
			t.Errorf("翻译占位符不一致: %s", key)
		}
	}
}

func TestChineseKeyIsSourceText(t *testing.T) {
	if got := Text("zh", "您于%s执行了%s", "12:00", "导出"); got != "您于12:00执行了导出" {
		t.Fatal(got)
	}
}
