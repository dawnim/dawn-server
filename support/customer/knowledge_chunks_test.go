package customer

import (
	"strings"
	"testing"
)

func TestSplitKnowledge(t *testing.T) {
	code := "```go\n# not a heading\n\n" + strings.Repeat("fmt.Println(1)\n", 150) + "```"
	entries := SplitKnowledge("安装", "# 安装\n\n- [环境要求](#环境要求)\n\n## 环境要求\n\n需要Go\n\n### 启动服务\n\n"+code+"\n\n启动后访问网站\n")
	if len(entries) != 3 || entries[0].Question != "安装-环境要求" || entries[1].Question != "安装-环境要求-启动服务-1" || entries[2].Question != "安装-环境要求-启动服务-2" {
		t.Fatalf("unexpected sections: %+v", entries)
	}
	if entries[1].Answer != code || strings.Contains(entries[0].Answer, "环境要求](") {
		t.Fatal("code split or table of contents retained")
	}
	plain := SplitKnowledge("说明", "first paragraph\n\nsecond paragraph")
	if len(plain) != 1 || plain[0].Question != "说明" || plain[0].Answer != "first paragraph\n\nsecond paragraph" {
		t.Fatal(plain)
	}
}

func TestDraftText(t *testing.T) {
	for input, want := range map[string]string{"建": "", "建议回复：您好": "您好", "**建议回复：**\n您好": "您好", "您好": "您好"} {
		if got := draftText(input); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}
