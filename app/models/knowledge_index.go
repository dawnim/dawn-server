package models

type KnowledgeIndex struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	AppID      uint   `json:"app_id" gorm:"not null;index;comment:所属客服应用ID"`
	Driver     string `json:"driver" gorm:"not null;size:24;comment:索引存储驱动"`
	Model      string `json:"model" gorm:"not null;size:255;comment:向量模型"`
	Dimensions int    `json:"dimensions" gorm:"not null;comment:向量维度"`
	Status     string `json:"status" gorm:"not null;size:24;index;comment:索引状态"`
	Total      int    `json:"total" gorm:"not null;default:0;comment:知识条目总数"`
	Completed  int    `json:"completed" gorm:"not null;default:0;comment:已构建条目数"`
	Error      string `json:"error" gorm:"not null;type:text;comment:构建或清理错误"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt  int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (KnowledgeIndex) TableName() string { return "knowledge_indexes" }

type KnowledgeVector struct {
	IndexID     uint   `gorm:"not null;primaryKey"`
	EntryID     uint   `gorm:"not null;primaryKey"`
	AppID       uint   `gorm:"not null;index"`
	Version     uint   `gorm:"not null"`
	ContentHash string `gorm:"not null;size:64"`
	Embedding   []byte `gorm:"not null"`
}

func (KnowledgeVector) TableName() string { return "knowledge_vectors" }
