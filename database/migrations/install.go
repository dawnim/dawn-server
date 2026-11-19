package migrations

import (
	"encoding/json"
	"github.com/dawnim/dawn-server/database/schema"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

// 首个系统表结构快照，独立于后续修改的业务模型。
type Base struct {
	ID        uint                  `gorm:"not null;primaryKey"`
	CreatedAt int64                 `gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64                 `gorm:"not null;autoUpdateTime:milli"`
	DeletedAt soft_delete.DeletedAt `gorm:"not null;softDelete:milli;index;default:0"`
}

type Department struct {
	Base
	Name     string `gorm:"not null;size:100;comment:部门名称"`
	ParentID uint   `gorm:"not null;default:0;index;comment:上级部门ID:0为顶级"`
	Leader   string `gorm:"not null;size:100;comment:负责人"`
	Phone    string `gorm:"not null;size:40;comment:联系电话"`
	Email    string `gorm:"not null;size:180;comment:联系邮箱"`
	Sort     int    `gorm:"not null;default:0;comment:排序:升序"`
	Status   int    `gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Remark   string `gorm:"not null;comment:备注;size:500"`
}

func (Department) TableName() string { return "admin_departments" }

type Post struct {
	Base
	Name   string `gorm:"not null;size:100;comment:岗位名称"`
	Code   string `gorm:"not null;size:100;uniqueIndex;comment:岗位唯一编码"`
	Sort   int    `gorm:"not null;default:0;comment:排序:升序"`
	Status int    `gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Remark string `gorm:"not null;comment:备注;size:500"`
}

func (Post) TableName() string { return "admin_posts" }

type Role struct {
	Base
	Name          string `gorm:"not null;size:100;comment:角色名称"`
	Code          string `gorm:"not null;size:100;uniqueIndex;comment:角色唯一编码"`
	Level         int    `gorm:"not null;default:1;comment:管理级别:数值越大级别越高"`
	Status        int    `gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Sort          int    `gorm:"not null;default:0;comment:排序:升序"`
	Remark        string `gorm:"not null;comment:备注;size:500"`
	DataScope     string `gorm:"not null;size:30;default:self;comment:数据范围:all全部 self本人 department本部门 departments指定部门"`
	DepartmentIDs []uint `gorm:"not null;serializer:json;default:'[]';comment:指定部门ID列表(JSON)"`
}

func (Role) TableName() string { return "admin_roles" }

type AdminUser struct {
	Base
	UserID       *uint  `gorm:"uniqueIndex;comment:关联普通用户ID"`
	Username     string `gorm:"not null;size:80;uniqueIndex;comment:登录账号"`
	Password     string `gorm:"not null;size:100;comment:密码哈希"`
	Realname     string `gorm:"not null;size:100;comment:姓名"`
	Email        string `gorm:"not null;size:180;comment:邮箱"`
	Phone        string `gorm:"not null;size:40;comment:手机号"`
	Avatar       string `gorm:"not null;size:500;comment:头像地址"`
	Gender       string `gorm:"not null;size:1;default:0;comment:性别:0未知 1男 2女"`
	DepartmentID uint   `gorm:"not null;default:0;index;comment:所属部门ID"`
	Status       int    `gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Super        bool   `gorm:"not null;default:false;comment:超级管理员:0否 1是"`
	Dashboard    string `gorm:"not null;size:20;default:statistics;comment:工作台类型:statistics统计页面 work员工工作台"`
	Remark       string `gorm:"not null;comment:备注;size:500"`
	LoginTime    int64  `gorm:"not null;default:0;comment:最后登录时间(Unix毫秒)"`
}

func (AdminUser) TableName() string { return "admin_users" }

type UserRole struct {
	User   AdminUser `gorm:"foreignKey:UserID"`
	Role   Role      `gorm:"foreignKey:RoleID"`
	UserID uint      `gorm:"not null;primaryKey;comment:管理员ID"`
	RoleID uint      `gorm:"not null;primaryKey;index;comment:角色ID"`
}

func (UserRole) TableName() string { return "admin_user_roles" }

type UserPost struct {
	User   AdminUser `gorm:"foreignKey:UserID"`
	Post   Post      `gorm:"foreignKey:PostID"`
	UserID uint      `gorm:"not null;primaryKey;comment:管理员ID"`
	PostID uint      `gorm:"not null;primaryKey;index;comment:岗位ID"`
}

func (UserPost) TableName() string { return "admin_user_posts" }

type RolePermission struct {
	RoleID uint   `gorm:"not null;primaryKey;comment:角色ID"`
	Key    string `gorm:"not null;size:150;primaryKey;comment:授权权限Key"`
}

func (RolePermission) TableName() string { return "admin_role_permissions" }

type Setting struct {
	ID        uint   `gorm:"primaryKey"`
	Key       string `gorm:"not null;size:100;uniqueIndex;comment:配置唯一标识"`
	Name      string `gorm:"not null;size:100;comment:配置名称"`
	Value     string `gorm:"not null;type:json;comment:配置内容JSON:保密字段保存密文"`
	Revision  int64  `gorm:"not null;default:1;comment:配置版本:防止并发写入旧缓存"`
	CreatedAt int64  `gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64  `gorm:"not null;autoUpdateTime:milli"`
	UpdatedBy uint   `gorm:"not null;default:0;comment:最后修改管理员ID"`
}

func (Setting) TableName() string { return "admin_settings" }

type Menu struct {
	Base
	Type      int    `gorm:"not null;default:0;comment:类型:1目录 2菜单 3按钮 4外链"`
	Path      string `gorm:"not null;size:500;comment:前端路由路径"`
	Component string `gorm:"not null;size:500;comment:前端组件路径"`
	Status    int    `gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Hidden    bool   `gorm:"not null;default:false;comment:隐藏菜单:0否 1是"`
	KeepAlive bool   `gorm:"not null;default:false;comment:缓存页面:0否 1是"`
	FixedTab  bool   `gorm:"not null;default:false;comment:固定页签:0否 1是"`
	FullPage  bool   `gorm:"not null;default:false;comment:独立全屏:0否 1是"`
	Key       string `gorm:"not null;size:150;uniqueIndex;comment:菜单或权限唯一Key:保存后不可修改"`
	ParentKey string `gorm:"not null;size:150;index;comment:上级菜单Key:空为顶级"`
	Title     string `gorm:"not null;size:100;comment:菜单或权限名称"`
	URL       string `gorm:"not null;size:1500;comment:菜单目标地址:站内路径或HTTP(S)地址"`
	Icon      string `gorm:"not null;size:100;comment:菜单图标"`
	Mode      string `gorm:"not null;size:20;comment:打开方式:iframe后台内嵌 new_tab新窗口"`
	SystemKey string `gorm:"not null;size:100;comment:受控外部系统Key"`
	Sort      int    `gorm:"not null;default:0;comment:排序:升序"`
}

func (Menu) TableName() string { return "admin_menus" }

type File struct {
	Location  string `gorm:"not null;serializer:json;type:text;comment:上传时的存储位置(JSON):驱动和路径参数,不含密钥"`
	ID        uint   `gorm:"not null;primaryKey"`
	MD5       string `gorm:"not null;size:32;index:idx_admin_file_lookup;comment:文件MD5摘要"`
	SHA256    string `gorm:"not null;size:64;uniqueIndex;comment:文件SHA-256摘要"`
	Size      int64  `gorm:"not null;default:0;index:idx_admin_file_lookup;comment:文件大小(字节)"`
	Path      string `gorm:"not null;size:500;comment:存储路径"`
	MIME      string `gorm:"not null;size:150;comment:文件媒体类型"`
	CreatedAt int64  `gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64  `gorm:"not null;autoUpdateTime:milli;comment:最后一次被内容引用的时间"`
	UsedCount int64  `gorm:"not null;default:0;comment:累计被内容引用次数"`
}

func (File) TableName() string { return "admin_files" }

type Category struct {
	Base
	Code     *string `gorm:"size:30;uniqueIndex;comment:自动分类标识，空值为自建分类"`
	Name     string  `gorm:"not null;size:100;comment:附件分类名称"`
	ParentID uint    `gorm:"not null;default:0;comment:上级分类ID:0为顶级;index"`
	Sort     int     `gorm:"not null;default:0;comment:排序:升序"`
}

func (Category) TableName() string { return "admin_categories" }

type Attachment struct {
	Base
	Scope      string `gorm:"not null;size:80;default:admin;uniqueIndex:idx_admin_attachment_file;comment:文件引用来源:admin或user:用户ID"`
	Name       string `gorm:"not null;size:255;comment:附件名称"`
	FileID     uint   `gorm:"not null;default:0;uniqueIndex:idx_admin_attachment_file;comment:文件ID"`
	CategoryID uint   `gorm:"not null;default:0;index;comment:附件分类ID:0为未分类"`
	CreatedBy  uint   `gorm:"not null;default:0;index;comment:上传者ID"`
	File       File   `gorm:"foreignKey:FileID"`
}

func (Attachment) TableName() string { return "admin_attachments" }

type Log struct {
	Location     string `gorm:"not null;default:'';size:255;comment:IP地理位置"`
	CountryCode  string `gorm:"not null;default:'';size:2;comment:IP所在国家代码"`
	RequestData  string `gorm:"not null;type:text;comment:请求参数:敏感字段脱敏"`
	ResponseData string `gorm:"not null;type:text;comment:响应摘要:敏感字段脱敏"`

	Sender    string `gorm:"not null;default:'';size:180;comment:邮件发件地址:非邮件日志为空"`
	Recipient string `gorm:"not null;default:'';size:180;comment:邮件收件地址:非邮件日志为空"`
	ID        uint   `gorm:"not null;primaryKey"`
	Kind      string `gorm:"not null;size:20;index:idx_admin_logs_kind_time;comment:日志类型:login登录 operation操作 email邮件"`
	UserID    uint   `gorm:"not null;default:0;index;comment:管理员ID"`
	Username  string `gorm:"not null;size:80;comment:管理员账号"`
	Method    string `gorm:"not null;size:10;comment:HTTP请求方法"`
	Path      string `gorm:"not null;size:500;comment:请求路径"`
	IP        string `gorm:"not null;size:80;comment:客户端IP"`
	Status    int    `gorm:"not null;default:0;comment:HTTP响应状态码"`
	Duration  int64  `gorm:"not null;default:0;comment:请求耗时(毫秒)"`
	Message   string `gorm:"not null;type:text;comment:日志内容"`
	CreatedAt int64  `gorm:"not null;autoCreateTime:milli;index:idx_admin_logs_kind_time"`
}

func (Log) TableName() string { return "admin_logs" }

type ChatConversation struct {
	ID              uint            `json:"id" gorm:"primaryKey"`
	Type            string          `json:"type" gorm:"not null;size:16;comment:会话类型:direct单聊 support客服"`
	AppID           uint            `json:"app_id" gorm:"not null;default:0;index:idx_support_inbox,priority:1;comment:客服应用ID:0为管理员私聊"`
	UserID          uint            `json:"user_id" gorm:"not null;default:0;index;comment:客户用户ID"`
	EntryKey        string          `json:"entry_key" gorm:"not null;default:'';size:80;comment:入口稳定标识"`
	Status          string          `json:"status" gorm:"not null;size:16;default:ai;index:idx_support_inbox,priority:2;comment:ai智能接待 waiting等待人工 human人工接待 closed已结束"`
	AssignedAdminID uint            `json:"assigned_admin_id" gorm:"not null;default:0;index;comment:当前负责管理员ID"`
	Version         uint            `json:"version" gorm:"not null;default:1;comment:接待并发控制版本"`
	Round           uint            `json:"round" gorm:"not null;default:1;comment:接待轮次"`
	Context         json.RawMessage `json:"context" gorm:"type:json;comment:渠道、外部消息、摘要与评价上下文"`
	Identify        string          `json:"-" gorm:"not null;size:180;uniqueIndex;comment:包含身份域的会话唯一标识"`
	LastMessageID   uint            `json:"last_message_id" gorm:"not null;default:0;index;comment:最后消息ID"`
	LastMessageAt   int64           `json:"last_message_at" gorm:"not null;default:0;comment:最后消息时间(Unix毫秒)"`
	CreatedAt       int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt       int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatConversation) TableName() string { return "chat_conversations" }

type ChatMember struct {
	ConversationID    uint            `json:"conversation_id" gorm:"primaryKey;comment:会话ID"`
	Realm             string          `json:"realm" gorm:"primaryKey;size:32;index:idx_chat_member_identity;comment:身份域:admin或业务定义"`
	Subject           string          `json:"subject" gorm:"primaryKey;size:64;index:idx_chat_member_identity;comment:身份域内的主体ID"`
	LastReadMessageID uint            `json:"last_read_message_id" gorm:"not null;default:0;comment:已读消息位置"`
	Extra             json.RawMessage `json:"-" gorm:"type:json;comment:内部输入预览与设备草稿快照"`
	CreatedAt         int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (ChatMember) TableName() string { return "chat_members" }

type ChatMessage struct {
	ExternalID     *string         `json:"-" gorm:"size:120;uniqueIndex;comment:外部渠道消息唯一标识"`
	ID             uint            `json:"id" gorm:"primaryKey;index:idx_chat_message_history,priority:2"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;index:idx_chat_message_history,priority:1;comment:会话ID"`
	SenderRealm    string          `json:"sender_realm" gorm:"not null;size:32;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:1;comment:实际发送者身份域"`
	SenderSubject  string          `json:"sender_subject" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:2;comment:实际发送者主体ID"`
	RequestID      string          `json:"request_id" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;comment:发送幂等标识"`
	Type           string          `json:"type" gorm:"not null;size:24;default:text;comment:消息类型:text文本 event事件"`
	Visibility     string          `json:"visibility" gorm:"not null;size:16;default:public;comment:public公开 internal内部 draft客服草稿"`
	Status         string          `json:"status" gorm:"not null;size:16;default:completed;comment:queued streaming completed failed cancelled"`
	Content        string          `json:"content" gorm:"not null;type:text;comment:消息正文"`
	ReplyToID      uint            `json:"reply_to_id" gorm:"not null;default:0;comment:同会话被回复消息ID"`
	Payload        json.RawMessage `json:"payload" gorm:"type:json;comment:按消息类型定义的扩展数据"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli;index:idx_chat_message_sender_time,priority:3"`
	UpdatedAt      int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatMessage) TableName() string { return "chat_messages" }

type ChatEvent struct {
	ID             uint            `json:"id" gorm:"primaryKey;index:idx_chat_event_pending,priority:3"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;index;comment:会话ID"`
	Type           string          `json:"type" gorm:"not null;size:64;comment:实时事件类型"`
	Data           json.RawMessage `json:"data" gorm:"not null;type:json;comment:公开事件数据"`
	Attempts       int             `json:"-" gorm:"not null;default:0;comment:投递尝试次数"`
	AvailableAt    int64           `json:"-" gorm:"not null;default:0;index:idx_chat_event_pending,priority:2;comment:下次投递或租约到期时间"`
	PublishedAt    int64           `json:"-" gorm:"not null;default:0;index:idx_chat_event_pending,priority:1;comment:发布到WS收件流的时间"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (ChatEvent) TableName() string { return "chat_events" }

type User struct {
	Base
	UniqueID         string  `json:"unique_id" gorm:"not null;size:32;uniqueIndex;comment:安装固定密钥生成的对外用户编号"`
	Type             string  `json:"type" gorm:"not null;size:16;default:guest;index;comment:用户类型:guest访客 member普通用户 ai智能体"`
	Username         *string `json:"username" gorm:"size:80;uniqueIndex;comment:本站登录用户名"`
	PasswordHash     string  `json:"-" gorm:"not null;default:'';size:100;comment:密码哈希"`
	Nickname         string  `json:"nickname" gorm:"not null;default:'';size:100;comment:用户昵称"`
	Avatar           string  `json:"avatar" gorm:"not null;default:'';size:500;comment:头像地址"`
	Email            string  `json:"email" gorm:"not null;default:'';size:180;comment:联系邮箱"`
	Mobile           string  `json:"mobile" gorm:"not null;default:'';size:40;comment:联系电话"`
	Locale           string  `json:"locale" gorm:"not null;size:16;default:zh;comment:偏好语言"`
	Status           int     `json:"status" gorm:"not null;default:1;comment:状态:1启用 2停用"`
	TelegramID       *int64  `json:"telegram_id" gorm:"uniqueIndex;comment:已绑定Telegram用户ID"`
	TelegramUsername string  `json:"telegram_username" gorm:"not null;default:'';size:80;comment:最近取得的Telegram用户名"`
}

func (User) TableName() string { return "users" }

type UserTraits struct {
	AppID            uint            `json:"app_id" gorm:"primaryKey;uniqueIndex:idx_traits_identity,priority:1;comment:客服应用ID"`
	UserID           uint            `json:"user_id" gorm:"primaryKey;comment:普通用户ID"`
	IdentityProvider *string         `json:"identity_provider" gorm:"size:100;uniqueIndex:idx_traits_identity,priority:2;comment:可信外部身份来源"`
	ExternalUserID   *string         `json:"external_user_id" gorm:"size:180;uniqueIndex:idx_traits_identity,priority:3;comment:已验证外部用户编号"`
	VerifiedAt       int64           `json:"verified_at" gorm:"not null;default:0;comment:最近身份验证时间(Unix毫秒)"`
	Attributes       json.RawMessage `json:"attributes" gorm:"type:json;comment:接入方提交的扩展资料"`
	Observed         json.RawMessage `json:"observed" gorm:"type:json;comment:访问环境与近期浏览轨迹"`
	Remark           string          `json:"remark" gorm:"not null;type:text;comment:应用内客服备注"`
	FirstSeenAt      int64           `json:"first_seen_at" gorm:"not null;autoCreateTime:milli;comment:首次访问时间(Unix毫秒)"`
	LastSeenAt       int64           `json:"last_seen_at" gorm:"not null;autoUpdateTime:milli;comment:最近访问时间(Unix毫秒)"`
}

func (UserTraits) TableName() string { return "user_traits" }

type SupportApp struct {
	Base
	Name          string          `json:"name" gorm:"not null;size:100;comment:客服应用名称"`
	CreatedBy     uint            `json:"created_by" gorm:"not null;default:0;index;comment:创建管理员ID"`
	PublicKey     string          `json:"public_key" gorm:"not null;size:64;uniqueIndex;comment:公开接入标识"`
	Avatar        string          `json:"avatar" gorm:"not null;default:'';size:500;comment:品牌头像"`
	AIUserID      uint            `json:"ai_user_id" gorm:"not null;comment:应用AI用户ID"`
	Status        int             `json:"status" gorm:"not null;default:1;comment:状态:1启用 2停用"`
	TelegramBotID *int64          `json:"telegram_bot_id" gorm:"uniqueIndex;comment:应用独占的Telegram机器人ID"`
	Config        json.RawMessage `json:"config" gorm:"type:json;comment:入口、模型、知识、工具与接待配置"`
	Secrets       string          `json:"-" gorm:"not null;type:text;comment:加密保存的模型、身份与机器人密钥"`
	Revision      uint            `json:"revision" gorm:"not null;default:1;comment:配置并发控制版本"`
}

func (SupportApp) TableName() string { return "support_apps" }

type KnowledgeSource struct {
	Base
	AppID       uint   `json:"app_id" gorm:"not null;index;comment:所属客服应用ID"`
	Name        string `json:"name" gorm:"not null;size:200;comment:资料名称"`
	Kind        string `json:"kind" gorm:"not null;size:24;comment:来源:text文本 url网页 markdown文档"`
	Content     string `json:"content" gorm:"not null;type:text;comment:原始资料或来源地址"`
	Status      string `json:"status" gorm:"not null;size:24;default:draft;comment:draft待处理 processing处理中 completed已处理 failed失败"`
	ContentHash string `json:"content_hash" gorm:"not null;default:'';size:64;comment:已处理内容SHA256"`
	Error       string `json:"error" gorm:"not null;type:text;comment:可公开的处理错误"`
}

func (KnowledgeSource) TableName() string { return "knowledge_sources" }

type KnowledgeEntry struct {
	Base
	AppID      uint            `json:"app_id" gorm:"not null;index:idx_knowledge_scope,priority:1;comment:所属客服应用ID"`
	SourceID   uint            `json:"source_id" gorm:"not null;default:0;index;comment:资料来源ID"`
	Question   string          `json:"question" gorm:"not null;size:1000;comment:知识标题或问题"`
	Answer     string          `json:"answer" gorm:"not null;type:text;comment:审核答案或原始文档分块"`
	Keywords   string          `json:"keywords" gorm:"not null;type:text;comment:关键词与相似问法"`
	Conditions string          `json:"conditions" gorm:"not null;type:text;comment:知识适用条件"`
	Locale     string          `json:"locale" gorm:"not null;size:16;default:zh;comment:知识语言"`
	ReplyMode  string          `json:"reply_mode" gorm:"not null;size:16;default:reference;comment:fixed固定原文 reference依据知识回答"`
	Status     string          `json:"status" gorm:"not null;size:16;default:draft;index:idx_knowledge_scope,priority:2;comment:draft待审核 published已发布 disabled停用"`
	Version    uint            `json:"version" gorm:"not null;default:1;comment:内容版本"`
	Experts    json.RawMessage `json:"experts" gorm:"type:json;comment:已绑定Telegram的协助用户ID列表"`
	Location   string          `json:"location" gorm:"not null;default:'';size:1000;comment:来源链接或标题位置"`
}

func (KnowledgeEntry) TableName() string { return "knowledge_entries" }

type AIRun struct {
	RequestKey     *string         `json:"-" gorm:"size:180;uniqueIndex;comment:跨渠道执行幂等标识"`
	ID             uint            `json:"id" gorm:"primaryKey"`
	AppID          uint            `json:"app_id" gorm:"not null;index;comment:所属客服应用ID"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;default:0;index;comment:所属会话ID"`
	MessageID      uint            `json:"message_id" gorm:"not null;default:0;comment:触发消息ID"`
	ReplyID        uint            `json:"reply_id" gorm:"not null;default:0;comment:回复消息ID"`
	ParentID       uint            `json:"parent_id" gorm:"not null;default:0;index;comment:父执行ID:0为根执行"`
	Kind           string          `json:"kind" gorm:"not null;size:32;comment:reply回复 draft人工草稿 import知识导入 index索引重建 tool工具"`
	Status         string          `json:"status" gorm:"not null;size:20;default:queued;index:idx_ai_pending,priority:1;comment:queued running completed failed cancelled waiting"`
	Version        uint            `json:"version" gorm:"not null;default:1;comment:绑定的会话或知识版本"`
	Attempt        uint            `json:"attempt" gorm:"not null;default:0;comment:领取次数与流式尝试编号"`
	LeaseToken     string          `json:"-" gorm:"not null;default:'';size:64;comment:领取者随机租约"`
	AvailableAt    int64           `json:"available_at" gorm:"not null;default:0;index:idx_ai_pending,priority:2;comment:可领取或租约到期时间(Unix毫秒)"`
	DeadlineAt     int64           `json:"deadline_at" gorm:"not null;default:0;comment:整体截止时间(Unix毫秒)"`
	Input          json.RawMessage `json:"input" gorm:"type:json;comment:经白名单处理的执行输入"`
	Output         json.RawMessage `json:"output" gorm:"type:json;comment:执行结果、来源与恢复检查点"`
	Error          string          `json:"error" gorm:"not null;type:text;comment:脱敏执行错误"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt      int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (AIRun) TableName() string { return "ai_runs" }

type SupportSession struct {
	ID                 uint   `json:"id" gorm:"primaryKey"`
	ConversationID     uint   `json:"conversation_id" gorm:"not null;uniqueIndex:idx_support_session_round;index;comment:聊天会话ID"`
	Round              uint   `json:"round" gorm:"not null;uniqueIndex:idx_support_session_round;comment:接待轮次"`
	AssignedAdminID    uint   `json:"assigned_admin_id" gorm:"not null;default:0;index;comment:负责客服ID"`
	Status             string `json:"status" gorm:"not null;size:20;default:waiting;index:idx_support_session_status;comment:waiting排队 active接待 closing_pending等待客户确认 closed结束 cancelled取消排队"`
	Version            uint   `json:"version" gorm:"not null;default:1;comment:接待状态版本"`
	Rating             *int   `json:"rating" gorm:"comment:评分1差评 2一般 3好评:空表示未评价"`
	Comment            string `json:"comment" gorm:"not null;type:text;comment:客户评价"`
	StartedAt          int64  `json:"started_at" gorm:"not null;default:0;comment:首次接待时间(Unix毫秒)，0表示尚未接待"`
	OfflineSince       int64  `json:"offline_since" gorm:"not null;default:0;comment:持续离线起点(Unix毫秒)，0表示未离线"`
	LastOnlineAt       int64  `json:"last_online_at" gorm:"not null;default:0;comment:最近确认在线时间(Unix毫秒)"`
	ClosingRequestedAt int64  `json:"closing_requested_at" gorm:"not null;default:0;comment:申请结束时间(Unix毫秒)"`
	CloseReason        string `json:"close_reason" gorm:"not null;default:'';size:32;comment:customer客户结束 offline_timeout离线结束 queue_offline_timeout离线取消排队"`
	ClosedAt           int64  `json:"closed_at" gorm:"not null;default:0;comment:结束时间(Unix毫秒)"`
	CreatedAt          int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt          int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (SupportSession) TableName() string { return "support_sessions" }

// Definition 返回完整的内置目标结构，业务可追加、替换或删除任何表定义。
func Definition() *schema.Definition {
	return &schema.Definition{Tables: map[string]schema.Table{
		(Department{}).TableName():       {Model: &Department{}, Comment: "系统:部门表"},
		(Post{}).TableName():             {Model: &Post{}, Comment: "系统:岗位表"},
		(Role{}).TableName():             {Model: &Role{}, Comment: "系统:角色表"},
		(AdminUser{}).TableName():        {Model: &AdminUser{}, Comment: "系统:管理员表"},
		(UserRole{}).TableName():         {Model: &UserRole{}, Comment: "系统:管理员与角色关联表"},
		(UserPost{}).TableName():         {Model: &UserPost{}, Comment: "系统:管理员与岗位关联表"},
		(RolePermission{}).TableName():   {Model: &RolePermission{}, Comment: "系统:角色权限表"},
		(Setting{}).TableName():          {Model: &Setting{}, Comment: "系统:全局配置表"},
		(Menu{}).TableName():             {Model: &Menu{}, Comment: "系统:自定义菜单与权限表"},
		(File{}).TableName():             {Model: &File{}, Comment: "系统:文件表"},
		(Category{}).TableName():         {Model: &Category{}, Comment: "系统:附件分类表"},
		(Attachment{}).TableName():       {Model: &Attachment{}, Comment: "系统:附件表"},
		(Log{}).TableName():              {Model: &Log{}, Comment: "系统:后台日志表"},
		(ChatConversation{}).TableName(): {Model: &ChatConversation{}, Comment: "系统:聊天会话表"},
		(ChatMember{}).TableName():       {Model: &ChatMember{}, Comment: "系统:聊天成员与已读位置表"},
		(ChatMessage{}).TableName():      {Model: &ChatMessage{}, Comment: "系统:聊天消息表"},
		(ChatEvent{}).TableName():        {Model: &ChatEvent{}, Comment: "系统:聊天事务推送待办表"},
		(User{}).TableName():             {Model: &User{}, Comment: "系统:普通用户表"},
		(UserTraits{}).TableName():       {Model: &UserTraits{}, Comment: "系统:用户特征表"},
		(SupportApp{}).TableName():       {Model: &SupportApp{}, Comment: "系统:客服应用表"},
		(SupportSession{}).TableName():   {Model: &SupportSession{}, Comment: "系统:客服接待表"},
		(KnowledgeSource{}).TableName():  {Model: &KnowledgeSource{}, Comment: "系统:知识来源表"},
		(KnowledgeEntry{}).TableName():   {Model: &KnowledgeEntry{}, Comment: "系统:知识条目表"},
		(AIRun{}).TableName():            {Model: &AIRun{}, Comment: "系统:AI执行表"},
	}, Keep: []string{"migrations", "sqlite_*", "knowledge_vectors_*"}}
}

// Up 创建首版系统表，由PrismGo迁移命令记录执行状态。
func Up(db *gorm.DB) error { return Definition().Create(db) }

// Down 只回滚本迁移定义的表，业务迁移由PrismGo分别回滚。
func Down(db *gorm.DB) error { return Definition().Drop(db) }
