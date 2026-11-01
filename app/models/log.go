package models

type Log struct {
	Location     string `json:"location" gorm:"not null;default:'';size:255;comment:IP地理位置"`
	CountryCode  string `json:"country_code" gorm:"not null;default:'';size:2;comment:IP所在国家代码"`
	RequestData  string `json:"request_data" gorm:"not null;type:text;comment:请求参数:敏感字段脱敏"`
	ResponseData string `json:"response_data" gorm:"not null;type:text;comment:响应摘要:敏感字段脱敏"`

	Sender    string `json:"sender" gorm:"not null;default:'';size:180;comment:邮件发件地址:非邮件日志为空"`
	Recipient string `json:"recipient" gorm:"not null;default:'';size:180;comment:邮件收件地址:非邮件日志为空"`
	ID        uint   `json:"id" gorm:"not null;primaryKey"`
	Kind      string `json:"kind" gorm:"not null;size:20;index:idx_admin_logs_kind_time;comment:日志类型:login登录 operation操作 email邮件"`
	UserID    uint   `json:"user_id" gorm:"not null;default:0;index;comment:管理员ID"`
	Username  string `json:"username" gorm:"not null;size:80;comment:管理员账号"`
	Method    string `json:"method" gorm:"not null;size:10;comment:HTTP请求方法"`
	Path      string `json:"path" gorm:"not null;size:500;comment:请求路径"`
	IP        string `json:"ip" gorm:"not null;size:80;comment:客户端IP"`
	Status    int    `json:"status" gorm:"not null;default:0;comment:HTTP响应状态码"`
	Duration  int64  `json:"duration" gorm:"not null;default:0;comment:请求耗时(毫秒)"`
	Message   string `json:"message" gorm:"not null;type:text;comment:日志内容"`
	CreatedAt int64  `json:"created_at" gorm:"not null;autoCreateTime:milli;index:idx_admin_logs_kind_time"`
}

func (Log) TableName() string { return "admin_logs" }
