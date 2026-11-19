package customer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillDirectoriesAndResources(t *testing.T) {
	m := &Manager{Storage: t.TempDir()}
	for _, scope := range []string{"app", "global"} {
		root := filepath.Join(m.SkillDirectory("example", scope), "product-help")
		if err := os.MkdirAll(filepath.Join(root, "references"), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: product-help\ndescription: Product documentation\n---\nRead references/api.md"), 0640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "references", "api.md"), []byte("API reference"), 0640); err != nil {
			t.Fatal(err)
		}
		skill, err := m.loadSkill("example", Skill{Name: "product-help", Scope: scope})
		if err != nil || skill.References["references/api.md"] != "API reference" {
			t.Fatalf("%s: %v %#v", scope, err, skill)
		}
	}
	if _, err := ParseSkill("---\nname: ../escape\ndescription: Invalid\n---\nText"); err == nil {
		t.Fatal("accepted traversal name")
	}
}

func TestKnowledgeRejectsPrivateURL(t *testing.T) {
	_, err := FetchKnowledge(context.Background(), "url", "http://127.0.0.1:1/private")
	if err == nil {
		t.Fatal("accepted private URL")
	}
	text, err := FetchKnowledge(context.Background(), "text", "Original content")
	if err != nil || text != "Original content" {
		t.Fatal(text, err)
	}
	_, err = FetchKnowledge(context.Background(), "url", "file:///etc/passwd")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
