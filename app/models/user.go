package models

type User struct {
	Base
	Username     string `json:"username" gorm:"not null;size:80;uniqueIndex;comment:登录账号" binding:"required,max=80"`
	Password     string `json:"-" gorm:"not null;size:100;comment:密码哈希"`
	Realname     string `json:"realname" gorm:"not null;size:100;comment:姓名"`
	Email        string `json:"email" gorm:"not null;size:180;comment:邮箱"`
	Phone        string `json:"phone" gorm:"not null;size:40;comment:手机号"`
	Avatar       string `json:"avatar" gorm:"not null;size:500;comment:头像地址"`
	Gender       string `json:"gender" gorm:"not null;size:1;default:0;comment:性别:0未知 1男 2女"`
	DepartmentID uint   `json:"dept_id" gorm:"not null;default:0;index;comment:所属部门ID"`
	Status       int    `json:"status" gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Super        bool   `json:"is_super" gorm:"not null;default:false;comment:超级管理员:0否 1是"`
	Remark       string `json:"remark" gorm:"not null;comment:备注;size:500"`
	LoginTime    int64  `json:"login_time" gorm:"not null;default:0;comment:最后登录时间(Unix毫秒)"`
	Roles        []Role `json:"role_list" gorm:"many2many:admin_user_roles"`
	Posts        []Post `json:"post_list" gorm:"many2many:admin_user_posts"`
}

func (User) TableName() string { return "admin_users" }
