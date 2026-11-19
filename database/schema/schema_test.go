package schema_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/database/schema"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schema.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := db.DB()
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { connection.Close() })
	return db
}

func TestInstalledDefinitionHasNoDiff(t *testing.T) {
	db := testDB(t)
	if err := migrations.Up(db); err != nil {
		t.Fatal(err)
	}
	plan, err := migrations.Definition().Check(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) > 0 {
		t.Fatalf("fresh installation differs: %+v", plan.Changes)
	}
}

type oldOrder struct {
	ID       uint   `gorm:"primaryKey"`
	Name     string `gorm:"type:varchar(12);not null;default:before;index:idx_order_name"`
	Obsolete string
	Count    *int
}
type order struct {
	ID    uint   `gorm:"primaryKey"`
	Name  string `gorm:"type:varchar(40);not null;default:after;uniqueIndex:idx_order_name"`
	Count int    `gorm:"not null;default:0;check:count_positive,count >= 0"`
	Note  string `gorm:"not null;default:hello"`
}

func TestCompleteSchemaSync(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &oldOrder{}}}, Keep: []string{"migrations", "sqlite_*"}}
	if err := old.Create(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO orders(id,name,obsolete,count) VALUES (9,'retained','discard',2)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE abandoned (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE migrations (migration TEXT); INSERT INTO migrations VALUES ('installed')").Error; err != nil {
		t.Fatal(err)
	}
	target := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &order{}}}, Keep: old.Keep}
	plan, err := target.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"drop_table", "drop_column", "alter_column", "add_column", "drop_index", "add_index", "add_constraint"} {
		if !slices.ContainsFunc(plan.Changes, func(c schema.Change) bool { return c.Action == action }) {
			t.Fatalf("missing %s: %+v", action, plan.Changes)
		}
	}
	if !db.Migrator().HasTable("abandoned") || !db.Migrator().HasColumn("orders", "obsolete") {
		t.Fatal("check modified database")
	}
	if err = target.Apply(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	plan, err = target.Check(ctx, db)
	if err != nil || len(plan.Changes) > 0 {
		t.Fatalf("not idempotent %+v %v", plan, err)
	}
	var saved order
	if err = db.Table("orders").First(&saved, 9).Error; err != nil || saved.Name != "retained" || saved.Note != "hello" || saved.Count != 2 {
		t.Fatal(saved, err)
	}
	if db.Migrator().HasTable("abandoned") || db.Migrator().HasColumn("orders", "obsolete") {
		t.Fatal("obsolete structure survived")
	}
	var migration string
	if err = db.Raw("SELECT migration FROM migrations").Scan(&migration).Error; err != nil || migration != "installed" {
		t.Fatal(migration, err)
	}
	if err = db.Exec("UPDATE orders SET count=-1").Error; err == nil {
		t.Fatal("check constraint was lost")
	}
	if err = db.Exec("INSERT INTO orders(name,count) VALUES ('retained',1)").Error; err == nil {
		t.Fatal("unique index was lost")
	}
}

func TestSQLiteFailedCopyRollsBack(t *testing.T) {
	db := testDB(t)
	old := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &oldOrder{}}}, Keep: []string{"sqlite_*"}}
	if err := old.Create(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO orders(name,count) VALUES ('one',NULL)").Error; err != nil {
		t.Fatal(err)
	}
	target := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &order{}}}, Keep: old.Keep}
	p, err := target.Check(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err = target.Apply(context.Background(), db, p); err == nil {
		t.Fatal("invalid retained NULL unexpectedly accepted")
	}
	if !db.Migrator().HasColumn("orders", "obsolete") {
		t.Fatal("failed synchronization was not rolled back")
	}
	var count int64
	db.Table("orders").Count(&count)
	if count != 1 {
		t.Fatal("failed sync lost rows")
	}
}

func TestSQLiteRebuildRetainsSequenceTriggersAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.Exec("PRAGMA foreign_keys=ON").Error; err != nil {
		t.Fatal(err)
	}
	old := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &oldOrder{}}}, Keep: []string{"sqlite_*", "audit"}}
	if err := old.Create(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE audit (name TEXT)", "CREATE TRIGGER record_order AFTER INSERT ON orders BEGIN INSERT INTO audit(name) VALUES (NEW.name); END", "INSERT INTO orders(id,name,count) VALUES (500,'deleted',1)", "DELETE FROM orders"} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	target := schema.Definition{Tables: map[string]schema.Table{"orders": {Model: &order{}}}, Keep: old.Keep}
	p, err := target.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = target.Apply(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO orders(name) VALUES ('retained trigger')").Error; err != nil {
		t.Fatal(err)
	}
	var saved order
	db.Table("orders").First(&saved)
	if saved.ID != 501 {
		t.Fatal("lost high-water mark", saved.ID)
	}
	var count int64
	db.Table("audit").Where("name=?", "retained trigger").Count(&count)
	if count != 1 {
		t.Fatal("lost trigger")
	}
	var enabled int
	db.Raw("PRAGMA foreign_keys").Scan(&enabled)
	if enabled != 1 {
		t.Fatal("foreign keys were left disabled")
	}
}

type parent struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func (parent) TableName() string { return "parents" }

type child struct {
	ID       uint `gorm:"primaryKey"`
	ParentID uint
	Parent   parent `gorm:"foreignKey:ParentID;constraint:OnDelete:CASCADE"`
}

func (child) TableName() string { return "children" }

type parentV2 struct {
	ID    uint `gorm:"primaryKey"`
	Name  string
	Extra string `gorm:"not null;default:extra"`
}

func TestForeignKeyRebuildAndRemovedTables(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	db.Exec("PRAGMA foreign_keys=ON")
	d := schema.Definition{Tables: map[string]schema.Table{"children": {Model: &child{}}, "parents": {Model: &parent{}}}, Keep: []string{"sqlite_*"}}
	if err := d.Create(db); err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO parents(id,name) VALUES (1,'keep')")
	db.Exec("INSERT INTO children(parent_id) VALUES (1)")
	d.Tables["parents"] = schema.Table{Model: &parentV2{}}
	p, err := d.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Apply(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	p, err = d.Check(ctx, db)
	if err != nil || len(p.Changes) > 0 {
		t.Fatal(p, err)
	}
	if err = db.Exec("INSERT INTO children(parent_id) VALUES (999)").Error; err == nil {
		t.Fatal("foreign key was lost")
	}
	delete(d.Tables, "children")
	delete(d.Tables, "parents")
	p, err = d.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Apply(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("parents") || db.Migrator().HasTable("children") {
		t.Fatal("removed tables survived")
	}
}

func TestSQLiteUndeclaredInlineConstraintsAreRemoved(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("CREATE TABLE records (id INTEGER PRIMARY KEY, name TEXT UNIQUE, count INTEGER CHECK(count>=0))").Error; err != nil {
		t.Fatal(err)
	}
	model := &struct {
		ID    int64 `gorm:"primaryKey;autoIncrement:false"`
		Name  string
		Count int
	}{}
	d := schema.Definition{Tables: map[string]schema.Table{"records": {Model: model}}, Keep: []string{"sqlite_*"}}
	p, err := d.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Changes, func(c schema.Change) bool { return c.Action == "drop_constraint" }) {
		t.Fatal("inline constraints were not reported", p)
	}
	if err = d.Apply(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO records(id,name,count) VALUES (1,'duplicate',-1),(2,'duplicate',-1)").Error; err != nil {
		t.Fatal("undeclared unique/check survived", err)
	}
	p, err = d.Check(ctx, db)
	if err != nil || len(p.Changes) > 0 {
		t.Fatal(p, err)
	}
}
