package admin

// LinkSystem 显式注册受信任的外部系统, 不向后台开放任意系统密钥编辑.
type LinkSystem struct {
	Key         string
	Origin      string
	Secret      string
	Permissions []string
}
