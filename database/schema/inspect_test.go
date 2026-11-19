package schema

import (
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Metadata comparisons use disconnected dialects; no external database is opened.
func TestDialectColumnComparison(t *testing.T) {
	cases := []struct {
		name     string
		dialect  gorm.Dialector
		model    any
		actual   column
		wantDiff bool
	}{
		{"mysql auto increment", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value uint64 `gorm:"primaryKey;autoIncrement"`
		}{}, column{typ: "bigint(20) unsigned", auto: true}, false},
		{"mysql missing auto increment", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value uint64 `gorm:"primaryKey;autoIncrement"`
		}{}, column{typ: "bigint(20) unsigned"}, true},
		{"mysql integer width", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value uint64 `gorm:"not null;default:0"`
		}{}, column{typ: "bigint(20) unsigned", def: ptr("0")}, false},
		{"mysql boolean alias", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value bool `gorm:"not null;default:false"`
		}{}, column{typ: "tinyint(1)", def: ptr("0")}, false},
		{"mysql longer varchar", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value string `gorm:"size:100;not null"`
		}{}, column{typ: "varchar(20)"}, true},
		{"mysql tighter null", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value int64 `gorm:"not null"`
		}{}, column{typ: "bigint", nullable: true}, true},
		{"mariadb json", mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/schema", SkipInitializeWithVersion: true}), &struct {
			Value string `gorm:"type:json"`
		}{}, column{typ: "longtext", nullable: true}, false},
		{"postgres alias", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=schema dbname=schema sslmode=disable"}), &struct {
			Value int64 `gorm:"not null;default:0"`
		}{}, column{typ: "int8", def: ptr("0")}, false},
		{"postgres string cast", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=schema dbname=schema sslmode=disable"}), &struct {
			Value string `gorm:"size:30;default:hello"`
		}{}, column{typ: "varchar(30)", nullable: true, def: ptr("'hello'::character varying")}, false},
		{"postgres decimal precision", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=schema dbname=schema sslmode=disable"}), &struct {
			Value float64 `gorm:"type:numeric(16,4)"`
		}{}, column{typ: "numeric(12,2)", nullable: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, err := gorm.Open(c.dialect, &gorm.Config{DisableAutomaticPing: true, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			conn, _ := db.DB()
			defer conn.Close()
			stmt := &gorm.Statement{DB: db}
			if err = stmt.Parse(c.model); err != nil {
				t.Fatal(err)
			}
			before, after := columnDiff(db, stmt.Schema.FieldsByDBName["value"], c.actual)
			if (before != after) != c.wantDiff {
				t.Fatalf("unexpected comparison: %s -> %s", before, after)
			}
		})
	}
}

func ptr(v string) *string { return &v }

func TestConstraintAndIndexNormalization(t *testing.T) {
	for _, pair := range [][2]string{
		{"FOREIGN KEY (`parent_id`) REFERENCES `parents` (`id`)", "FOREIGN KEY (parent_id) REFERENCES parents(id) ON DELETE RESTRICT ON UPDATE NO ACTION"},
		{"CHECK ((count >= 0))", "CHECK (count >= 0)"},
		{"UNIQUE (`name`)", "UNIQUE (\"name\")"},
	} {
		if normalize(pair[0]) != normalize(pair[1]) {
			t.Fatal(pair)
		}
	}
	if normalize("CHECK (status='A')") == normalize("CHECK (status='a')") {
		t.Fatal("case-sensitive literal was erased")
	}
	if !strings.Contains(storedIndexDefinition("CREATE INDEX name ON public.orders USING hash (name)", false), "USING hash") {
		t.Fatal("index method was erased")
	}
}
