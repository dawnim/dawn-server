package models

type KnowledgeSource struct {
	Base
	AppID       uint   `json:"app_id" gorm:"not null;index;comment:所属客服应用ID"`
	Name        string `json:"name" gorm:"not null;size:200;comment:资料名称"`
	Kind        string `json:"kind" gorm:"not null;size:24;comment:来源:text文本 url网页 markdown文档"`
	Content     string `json:"content" gorm:"not null;type:text;comment:原始资料或来源地址"`
	Status      string `json:"status" gorm:"not null;size:24;default:draft;comment:draft待处理 queued排队中 processing处理中 completed已处理 failed失败"`
	ContentHash string `json:"content_hash" gorm:"not null;default:'';size:64;comment:已处理内容SHA256"`
	Error       string `json:"error" gorm:"not null;type:text;comment:可公开的处理错误"`
}

func (KnowledgeSource) TableName() string { return "knowledge_sources" }
