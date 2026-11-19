package customer

import (
	"fmt"
	"strings"

	"github.com/dawnim/dawn-server/app/models"
)

// SplitKnowledge 按Markdown章节与段落分块，代码围栏保持完整，标题保留文档归属。
func SplitKnowledge(name, text string) []models.KnowledgeEntry {
	var entries []models.KnowledgeEntry
	headings := [6]string{}
	title := name
	var block, chunk strings.Builder
	fence := ""
	flushChunk := func() {
		if answer := strings.TrimSpace(chunk.String()); answer != "" {
			entries = append(entries, models.KnowledgeEntry{Question: title, Answer: answer})
		}
		chunk.Reset()
	}
	flushBlock := func() {
		value := strings.TrimSpace(block.String())
		block.Reset()
		if value == "" {
			return
		}
		if chunk.Len() > 0 && len([]rune(chunk.String()))+len([]rune(value)) > 1200 {
			flushChunk()
		}
		if chunk.Len() > 0 {
			chunk.WriteString("\n\n")
		}
		chunk.WriteString(value)
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			flushBlock()
			fence = strings.Repeat(trimmed[:1], len(trimmed)-len(strings.TrimLeft(trimmed, trimmed[:1])))
		} else if fence != "" && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
			block.WriteString(line)
			block.WriteByte('\n')
			fence = ""
			flushBlock()
			continue
		} else if fence == "" {
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if level > 0 && level <= 6 && len(trimmed) > level && trimmed[level] == ' ' {
				flushBlock()
				flushChunk()
				headings[level-1] = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(trimmed[level:]), "#"))
				clear(headings[level:])
				parts := []string{name}
				for _, heading := range headings {
					if heading != "" && heading != name && heading != parts[len(parts)-1] && !strings.HasSuffix(parts[len(parts)-1], "-"+heading) {
						parts = append(parts, heading)
					}
				}
				title = strings.Join(parts, "-")
				continue
			}
			if trimmed == "" {
				flushBlock()
				continue
			}
			// 文档自动目录只重复标题，不作为答案导入。
			if (strings.HasPrefix(trimmed, "- [") || strings.HasPrefix(trimmed, "* [")) && strings.Contains(trimmed, "](#") {
				continue
			}
		}
		block.WriteString(line)
		block.WriteByte('\n')
	}
	flushBlock()
	flushChunk()
	totals := map[string]int{}
	for _, entry := range entries {
		totals[entry.Question]++
	}
	seen := map[string]int{}
	for i := range entries {
		title := entries[i].Question
		seen[title]++
		if totals[title] > 1 {
			entries[i].Question = fmt.Sprintf("%s-%d", title, seen[title])
		}
		runes := []rune(entries[i].Question)
		entries[i].Question = string(runes[:min(len(runes), 1000)])
	}
	return entries
}
