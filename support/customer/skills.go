package customer

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SkillDirectory 返回全局或应用Skill目录，应用目录使用公开应用标识。
func (m *Manager) SkillDirectory(appKey, scope string) string {
	if scope == "global" {
		return filepath.Join(m.Storage, "support", "skills")
	}
	return filepath.Join(m.Storage, "support", "apps", appKey, "skills")
}

func (m *Manager) loadSkill(appKey string, ref Skill) (Skill, error) {
	root := filepath.Join(m.SkillDirectory(appKey, ref.Scope), ref.Name)
	data, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil {
		return ref, err
	}
	skill, err := ParseSkill(string(data))
	if err != nil || skill.Name != ref.Name {
		return ref, ErrInvalid
	}
	skill.Scope = ref.Scope
	skill.References = map[string]string{}
	err = filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if entry.IsDir() || entry.Name() == "SKILL.md" {
			return nil
		}
		name, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err == nil {
			skill.References[filepath.ToSlash(name)] = string(data)
		}
		return err
	})
	return skill, err
}

// ProposeSkill 在管理员确认前只生成安装预览，不写文件或执行脚本。
func (m *Manager) ProposeSkill(ctx context.Context, appID uint, prompt string) (string, error) {
	_, cfg, secrets, err := m.App(ctx, appID)
	if err != nil {
		return "", err
	}
	result, err := m.modelTurn(ctx, cfg, secrets.APIKey, "根据管理员需求编写Agent Skills的SKILL.md。只输出完整Markdown，必须以---开头，包含name和description的YAML头。名称使用小写英文和连字符。不得声称已安装，不生成shell命令，不授予未注册工具权限。", []modelMessage{{Role: "user", Content: prompt}}, nil, func(string) error { return nil })
	return strings.TrimSpace(result.Content), err
}
