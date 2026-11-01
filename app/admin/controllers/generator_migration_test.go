package controllers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type migrationFixture struct {
	ID        uint64  `gorm:"primaryKey;autoIncrement"`
	Name      string  `gorm:"size:100;not null;uniqueIndex:ux_codegen_name_code,priority:1;comment:名称"`
	Code      string  `gorm:"size:50;not null;uniqueIndex:ux_codegen_name_code,priority:2;comment:编码\\;含反引号"`
	Remark    *string `gorm:"type:text;comment:备注"`
	Sort      int64   `gorm:"not null;default:100;index:ix_codegen_sort"`
	Status    int64   `gorm:"not null;default:1"`
	CreatedAt int64   `gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64   `gorm:"not null;autoUpdateTime:milli"`
	DeletedAt int64   `gorm:"not null;default:0"`
}

func (migrationFixture) TableName() string { return "qa_codegen_roundtrip" }

// 实际编译并执行生成文件，验证快照不会受当前业务模型影响。
func TestGeneratedMigrationRoundTrip(t *testing.T) {
	root := t.TempDir()
	driver, dsn := os.Getenv("DAWN_TEST_DRIVER"), os.Getenv("DAWN_TEST_DSN")
	var dialect gorm.Dialector
	switch driver {
	case "mysql", "mariadb":
		if dsn == "" {
			t.Skip("需要专用空库DSN")
		}
		dialect = mysql.Open(dsn)
	case "pgsql":
		if dsn == "" {
			t.Skip("需要专用空库DSN")
		}
		dialect = postgres.Open(dsn)
	default:
		driver = "sqlite"
		dsn = filepath.Join(root, "roundtrip.sqlite")
		dialect = sqlite.Open(dsn)
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if db.Migrator().HasTable(&migrationFixture{}) {
		t.Fatal("测试表已存在，需要独立空库")
	}
	if err := db.AutoMigrate(&migrationFixture{}); err != nil {
		t.Fatal(err)
	}
	defer db.Migrator().DropTable(&migrationFixture{})
	comment := "业务记录表"
	switch driver {
	case "mysql", "mariadb":
		err = db.Exec("ALTER TABLE ? COMMENT = ?", clause.Table{Name: "qa_codegen_roundtrip"}, gorm.Expr(db.Dialector.Explain("?", comment))).Error
	case "pgsql":
		err = db.Exec("COMMENT ON TABLE ? IS ?", clause.Table{Name: "qa_codegen_roundtrip"}, gorm.Expr(db.Dialector.Explain("$1", comment))).Error
	}
	if err != nil {
		t.Fatal(err)
	}
	columns, err := db.Migrator().ColumnTypes("qa_codegen_roundtrip")
	if err != nil {
		t.Fatal(err)
	}
	source, err := generateMigration(db, "qa_codegen_roundtrip", columns)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&migrationFixture{}); err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile("../../../go.sum")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"go.mod": []byte(strings.Replace(string(module), "module github.com/dawnim/dawn-server", "module example.com/generated-migration", 1)), "go.sum": sum, "generated.go": source, "generated_test.go": []byte(generatedMigrationTest)} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "test", "-mod=mod", "-count=1", ".")
	command.Dir = root
	command.Env = append(os.Environ(), "DAWN_TEST_DRIVER="+driver, "DAWN_TEST_DSN="+dsn)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("生成迁移执行失败: %v\n%s\n%s", err, output, source)
	}
}

const generatedMigrationTest = `package migrations
import("os";"testing";"github.com/prismgo/framework/database";"github.com/glebarez/sqlite";"gorm.io/driver/mysql";"gorm.io/driver/postgres";"gorm.io/gorm")
func TestSnapshot(t *testing.T){
 driver,dsn:=os.Getenv("DAWN_TEST_DRIVER"),os.Getenv("DAWN_TEST_DSN")
 var dialect gorm.Dialector
 switch driver{case "mysql","mariadb":dialect=mysql.Open(dsn);case "pgsql":dialect=postgres.Open(dsn);default:dialect=sqlite.Open(dsn)}
 db,err:=gorm.Open(dialect,&gorm.Config{});if err!=nil{t.Fatal(err)}
 conn,_:=db.DB();defer conn.Close()
 entry,ok:=database.MigrationByName("generated");if !ok{t.Fatal("未注册迁移")}
 if err:=entry.Up(db);err!=nil{t.Fatal(err)}
 if err:=db.Exec("INSERT INTO qa_codegen_roundtrip(name,code,created_at,updated_at) VALUES('测试','A',1,1)").Error;err!=nil{t.Fatal(err)}
 if err:=entry.Up(db);err!=nil{t.Fatal(err)}
 var row struct{ID uint64;Sort int;Status int;Remark *string}
 if err:=db.Table("qa_codegen_roundtrip").First(&row).Error;err!=nil{t.Fatal(err)}
 if row.ID==0||row.Sort!=100||row.Status!=1||row.Remark!=nil{t.Fatalf("字段或默认值丢失: %+v",row)}
 if err:=db.Exec("INSERT INTO qa_codegen_roundtrip(name,code,created_at,updated_at) VALUES('测试','A',1,1)").Error;err==nil{t.Fatal("联合唯一索引丢失")}
 if !db.Migrator().HasIndex("qa_codegen_roundtrip","ix_codegen_sort"){t.Fatal("普通索引丢失")}
 columns,err:=db.Migrator().ColumnTypes("qa_codegen_roundtrip");if err!=nil{t.Fatal(err)}
 for _,column:=range columns{if column.Name()=="name"{if length,ok:=column.Length();ok&&length!=100{t.Fatal("字段长度丢失",length)};if nullable,_:=column.Nullable();nullable{t.Fatal("非空约束丢失")}}}
 if driver=="mysql"||driver=="mariadb"||driver=="pgsql"{
  for _,column:=range columns{if column.Name()=="code"{if comment,_:=column.Comment();comment!="编码;含反引号"{t.Fatal("字段注释丢失",comment)}}}
  comment:=""
  if driver=="pgsql"{err=db.Raw("SELECT obj_description(to_regclass('qa_codegen_roundtrip'))").Scan(&comment).Error}else{info,e:=db.Migrator().TableType("qa_codegen_roundtrip");err=e;if err==nil{comment,_=info.Comment()}}
  if err!=nil||comment!="业务记录表"{t.Fatal("表注释丢失",comment,err)}
 }
 if err:=entry.Down(db);err!=nil{t.Fatal(err)}
 if db.Migrator().HasTable("qa_codegen_roundtrip"){t.Fatal("回滚失败")}
}
`
