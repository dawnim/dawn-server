package migrations

import (
	"encoding/json"
	"github.com/prismgo/framework/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

type User struct {
	Base
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
	Roles        []Role `gorm:"many2many:admin_user_roles"`
	Posts        []Post `gorm:"many2many:admin_user_posts"`
}

func (User) TableName() string { return "admin_users" }

type UserRole struct {
	UserID uint `gorm:"not null;primaryKey;comment:管理员ID"`
	RoleID uint `gorm:"not null;primaryKey;index;comment:角色ID"`
}

func (UserRole) TableName() string { return "admin_user_roles" }

type UserPost struct {
	UserID uint `gorm:"not null;primaryKey;comment:管理员ID"`
	PostID uint `gorm:"not null;primaryKey;index;comment:岗位ID"`
}

func (UserPost) TableName() string { return "admin_user_posts" }

type RolePermission struct {
	RoleID uint   `gorm:"not null;primaryKey;comment:角色ID"`
	Key    string `gorm:"not null;size:150;primaryKey;comment:授权权限Key"`
}

func (RolePermission) TableName() string { return "admin_role_permissions" }

type Session struct {
	ID               string `gorm:"not null;size:64;primaryKey"`
	UserID           uint   `gorm:"not null;default:0;index;comment:管理员ID"`
	AccessHash       string `gorm:"not null;size:64;uniqueIndex;comment:访问凭证摘要"`
	RefreshHash      string `gorm:"not null;size:64;uniqueIndex;comment:刷新凭证摘要"`
	ExpiresAt        int64  `gorm:"not null;default:0;index;comment:访问凭证到期时间(Unix毫秒)"`
	RefreshExpiresAt int64  `gorm:"not null;default:0;index;comment:刷新凭证到期时间(Unix毫秒)"`
	CreatedAt        int64  `gorm:"not null;autoCreateTime:milli"`
	IP               string `gorm:"not null;size:80;comment:登录IP"`
	UserAgent        string `gorm:"not null;size:500;comment:客户端信息"`
}

func (Session) TableName() string { return "admin_sessions" }

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
	Scope      string `gorm:"not null;size:80;default:admin;uniqueIndex:idx_admin_attachment_file;comment:文件引用来源:admin为后台附件库"`
	Name       string `gorm:"not null;size:255;comment:附件名称"`
	FileID     uint   `gorm:"not null;default:0;uniqueIndex:idx_admin_attachment_file;comment:文件ID"`
	CategoryID uint   `gorm:"not null;default:0;index;comment:附件分类ID:0为未分类"`
	CreatedBy  uint   `gorm:"not null;default:0;index;comment:上传管理员ID"`
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
	ID            uint   `json:"id" gorm:"primaryKey"`
	Type          string `json:"type" gorm:"not null;size:16;comment:会话类型:direct单聊 support客服"`
	Identify      string `json:"-" gorm:"not null;size:180;uniqueIndex;comment:包含身份域的会话唯一标识"`
	LastMessageID uint   `json:"last_message_id" gorm:"not null;default:0;index;comment:最后消息ID"`
	LastMessageAt int64  `json:"last_message_at" gorm:"not null;default:0;comment:最后消息时间(Unix毫秒)"`
	CreatedAt     int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt     int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatConversation) TableName() string { return "chat_conversations" }

type ChatMember struct {
	ConversationID    uint   `json:"conversation_id" gorm:"primaryKey;comment:会话ID"`
	Realm             string `json:"realm" gorm:"primaryKey;size:32;index:idx_chat_member_identity;comment:身份域:admin或业务定义"`
	Subject           string `json:"subject" gorm:"primaryKey;size:64;index:idx_chat_member_identity;comment:身份域内的主体ID"`
	LastReadMessageID uint   `json:"last_read_message_id" gorm:"not null;default:0;comment:已读消息位置"`
	CreatedAt         int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (ChatMember) TableName() string { return "chat_members" }

type ChatMessage struct {
	ID             uint            `json:"id" gorm:"primaryKey;index:idx_chat_message_history,priority:2"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;index:idx_chat_message_history,priority:1;comment:会话ID"`
	SenderRealm    string          `json:"sender_realm" gorm:"not null;size:32;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:1;comment:实际发送者身份域"`
	SenderSubject  string          `json:"sender_subject" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:2;comment:实际发送者主体ID"`
	RequestID      string          `json:"request_id" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;comment:发送幂等标识"`
	Type           string          `json:"type" gorm:"not null;size:24;default:text;comment:消息类型:text文本 event事件"`
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

type table struct {
	model   interface{ TableName() string }
	comment string
}

var tables = []table{
	{&Department{}, "部门"},
	{&Post{}, "岗位"},
	{&Role{}, "角色"},
	{&User{}, "管理员"},
	{&UserRole{}, "管理员与角色关联"},
	{&UserPost{}, "管理员与岗位关联"},
	{&RolePermission{}, "角色权限"},
	{&Session{}, "管理员登录会话"},
	{&Setting{}, "全局配置"},
	{&Menu{}, "自定义菜单与权限"},
	{&File{}, "文件"},
	{&Category{}, "附件分类"},
	{&Attachment{}, "附件"},
	{&Log{}, "后台日志"},
	{&ChatConversation{}, "聊天会话"},
	{&ChatMember{}, "聊天成员与已读位置"},
	{&ChatMessage{}, "聊天消息"},
	{&ChatEvent{}, "聊天事务推送待办"},
}

// Up 创建首版系统表，由迁移命令记录执行状态。
func Up(db *gorm.DB) error {
	if err := db.SetupJoinTable(&User{}, "Roles", &UserRole{}); err != nil {
		return err
	}
	if err := db.SetupJoinTable(&User{}, "Posts", &UserPost{}); err != nil {
		return err
	}
	for _, table := range tables {
		if err := db.Set("gorm:table_options", database.TableOptions(db.Dialector.Name(), "")).Migrator().CreateTable(table.model); err != nil {
			return err
		}
		var err error
		switch db.Dialector.Name() {
		case "mysql":
			err = db.Exec("ALTER TABLE ? COMMENT = ?", clause.Table{Name: table.model.TableName()}, gorm.Expr(db.Dialector.Explain("?", table.comment))).Error
		case "postgres":
			err = db.Exec("COMMENT ON TABLE ? IS ?", clause.Table{Name: table.model.TableName()}, gorm.Expr(db.Dialector.Explain("$1", table.comment))).Error
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Down 按依赖顺序删除系统表，不影响项目自己的业务表。
func Down(db *gorm.DB) error {
	for i := len(tables) - 1; i >= 0; i-- {
		if err := db.Migrator().DropTable(tables[i].model); err != nil {
			return err
		}
	}
	return nil
}
