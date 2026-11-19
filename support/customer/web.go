package customer

import "embed"

// Web 包含独立访客页和无框架依赖的嵌入脚本，两个入口共用同一聊天应用。
//
//go:embed web/*
var Web embed.FS
