package models

import "encoding/json"

type KnowledgeEntry struct {
	Base
	AppID      uint            `json:"app_id" gorm:"not null;index:idx_knowledge_scope,priority:1;comment:所属客服应用ID"`
	SourceID   uint            `json:"source_id" gorm:"not null;default:0;index;comment:资料来源ID"`
	Question   string          `json:"question" gorm:"not null;size:1000;comment:知识标题或问题"`
	Answer     string          `json:"answer" gorm:"not null;type:text;comment:审核答案或原始文档分块"`
	Keywords   string          `json:"keywords" gorm:"not null;default:'';type:text;comment:关键词与相似问法"`
	Conditions string          `json:"conditions" gorm:"not null;default:'';type:text;comment:知识适用条件"`
	Locale     string          `json:"locale" gorm:"not null;size:16;default:zh;comment:知识语言"`
	ReplyMode  string          `json:"reply_mode" gorm:"not null;size:16;default:reference;comment:fixed固定原文 reference依据知识回答"`
	Status     string          `json:"status" gorm:"not null;size:16;default:draft;index:idx_knowledge_scope,priority:2;comment:draft待审核 published已发布 disabled停用"`
	Version    uint            `json:"version" gorm:"not null;default:1;comment:内容版本"`
	Experts    json.RawMessage `json:"experts" gorm:"type:json;comment:已绑定Telegram的协助用户ID列表"`
	Location   string          `json:"location" gorm:"not null;default:'';size:1000;comment:来源链接或标题位置"`
}

func (KnowledgeEntry) TableName() string { return "knowledge_entries" }
