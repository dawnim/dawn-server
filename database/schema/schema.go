// Package schema compares a complete application schema with its database.
package schema

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/database"
	"github.com/prismgo/framework/facade"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	gs "gorm.io/gorm/schema"
)

// Table uses ordinary GORM fields, indexes and constraints as the target definition.
type Table struct {
	Model   any
	Comment string
}

// Definition is the complete database target. Tables absent from it are dropped.
// Keep contains runtime-owned table patterns, not a boundary for business tables.
type Definition struct {
	Tables map[string]Table
	Keep   []string
}

// Resolve returns the current application target, including business customizations.
func Resolve() *Definition { return facade.Resolve[*Definition]("dawn.schema") }

// Change describes one planned operation. Risk marks operations that can affect data.
type Change struct {
	Action string `json:"action"`
	Table  string `json:"table"`
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Risk   bool   `json:"risk"`
}

// Plan is read-only; Apply executes these operations against the same definition.
type Plan struct {
	Changes []Change `json:"changes"`
}

func (d *Definition) ordered(db *gorm.DB) ([]string, map[string]*gorm.Statement, error) {
	names := make([]string, 0, len(d.Tables))
	statements := map[string]*gorm.Statement{}
	for name, table := range d.Tables {
		s := &gorm.Statement{DB: db}
		if err := s.ParseWithSpecialTableName(table.Model, name); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		statements[name] = s
	}
	slices.Sort(names)
	ordered := []string{}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		refs := []string{}
		for _, rel := range statements[name].Schema.Relationships.Relations {
			if c := rel.ParseConstraint(); c != nil && c.Schema == statements[name].Schema && statements[c.ReferenceSchema.Table] != nil {
				refs = append(refs, c.ReferenceSchema.Table)
			}
		}
		slices.Sort(refs)
		for _, ref := range refs {
			visit(ref)
		}
		ordered = append(ordered, name)
	}
	for _, name := range names {
		visit(name)
	}
	return ordered, statements, nil
}

// Create creates the complete target, without seeding data or recording migrations.
func (d *Definition) Create(db *gorm.DB) error {
	names, _, err := d.ordered(db)
	if err != nil {
		return err
	}
	for _, name := range names {
		table := d.Tables[name]
		if err = db.Set("gorm:table_options", database.TableOptions(db.Dialector.Name(), "")).Table(name).Migrator().CreateTable(table.Model); err != nil {
			return err
		}
		if err = setComment(db, name, table.Comment); err != nil {
			return err
		}
	}
	return nil
}

// Drop drops definition tables in reverse dependency order.
func (d *Definition) Drop(db *gorm.DB) error {
	names, _, err := d.ordered(db)
	if err != nil {
		return err
	}
	slices.Reverse(names)
	for _, name := range names {
		if err = db.Migrator().DropTable(name); err != nil {
			return err
		}
	}
	return nil
}

// Check compares all tables; an optional selection limits this invocation only.
func (d *Definition) Check(ctx context.Context, db *gorm.DB, selection ...string) (*Plan, error) {
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	names, statements, err := d.ordered(db)
	if err != nil {
		return nil, err
	}
	actual, err := db.Migrator().GetTables()
	if err == nil && db.Dialector.Name() == "mysql" {
		err = db.Raw("SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE'").Scan(&actual).Error
	}
	if err != nil {
		return nil, err
	}
	slices.Sort(actual)
	for _, name := range selection {
		if _, ok := d.Tables[name]; !ok && !slices.Contains(actual, name) {
			return nil, i18n.Errorf("未找到指定表：%s", name)
		}
	}
	p := &Plan{Changes: []Change{}}
	type foreignKey struct {
		table string
		constraint
	}
	foreignKeys := []foreignKey{}
	selected := func(name string) bool { return len(selection) == 0 || slices.Contains(selection, name) }
	for _, name := range names {
		if !selected(name) {
			continue
		}
		table := d.Tables[name]
		s := statements[name]
		tx := db.Table(name)
		if !slices.Contains(actual, name) {
			p.Changes = append(p.Changes, Change{Action: "create_table", Table: name, After: table.Comment})
			continue
		}
		columns, err := readColumns(tx, s)
		if err != nil {
			return nil, err
		}
		for _, fieldName := range s.Schema.DBNames {
			f := s.Schema.FieldsByDBName[fieldName]
			if f.IgnoreMigration {
				continue
			}
			col, ok := columns[fieldName]
			if !ok {
				p.Changes = append(p.Changes, Change{Action: "add_column", Table: name, Name: fieldName, After: tx.Migrator().FullDataTypeOf(f).SQL, Risk: f.NotNull && !f.HasDefaultValue && !f.AutoIncrement})
				continue
			}
			if before, after := columnDiff(tx, f, col); before != after {
				p.Changes = append(p.Changes, Change{Action: "alter_column", Table: name, Name: fieldName, Before: before, After: after, Risk: true})
			}
		}
		extras := []string{}
		for col := range columns {
			if f := s.Schema.FieldsByDBName[col]; f == nil {
				extras = append(extras, col)
			}
		}
		slices.Sort(extras)
		for _, col := range extras {
			p.Changes = append(p.Changes, Change{Action: "drop_column", Table: name, Name: col, Risk: true})
		}
		constraints, err := readConstraints(tx, name)
		if err != nil {
			return nil, err
		}
		desired := targetConstraints(tx, s)
		if db.Dialector.Name() == "mysql" {
			for key, c := range constraints {
				if c.Kind == "FOREIGN KEY" && normalize(c.Definition) == normalize(desired[key].Definition) {
					foreignKeys = append(foreignKeys, foreignKey{name, desired[key]})
				}
				if c.Kind == "UNIQUE" && s.Schema.LookIndex(key) != nil {
					delete(constraints, key)
				}
			}
		}
		keys := []string{}
		for key := range constraints {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			c := constraints[key]
			if want, ok := desired[key]; !ok || normalize(c.Definition) != normalize(want.Definition) {
				p.Changes = append(p.Changes, Change{Action: "drop_constraint", Table: name, Name: c.Name, Before: c.Definition, Risk: true})
			}
		}
		keys = keys[:0]
		for key := range desired {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			c := desired[key]
			if old, ok := constraints[key]; !ok || normalize(c.Definition) != normalize(old.Definition) {
				p.Changes = append(p.Changes, Change{Action: "add_constraint", Table: name, Name: c.Name, After: c.Definition, Risk: true})
			}
		}
		indexes, err := readIndexes(tx, name)
		if err != nil {
			return nil, err
		}
		wanted := s.Schema.ParseIndexes()
		wantIndexes := map[string]*gs.Index{}
		for _, idx := range wanted {
			wantIndexes[idx.Name] = idx
		}
		implicit := map[string]bool{}
		if db.Dialector.Name() == "mysql" {
			for _, c := range desired {
				if c.Kind != "FOREIGN KEY" {
					continue
				}
				fields := sqlColumns(c.Definition)
				covered := slices.ContainsFunc(wanted, func(idx *gs.Index) bool { return indexCovers(indexDefinition(idx), fields) })
				for _, key := range desired {
					covered = covered || (key.Kind == "PRIMARY KEY" || key.Kind == "UNIQUE") && indexCovers(key.Definition, fields)
				}
				if covered {
					continue
				}
				// Preserve one database-created supporting index absent from GORM's tags.
				for _, idx := range indexes {
					if !idx.primary && !idx.unique && wantIndexes[idx.name] == nil && indexCovers(idx.definition, fields) {
						implicit[idx.name] = true
						break
					}
				}
			}
		}
		for _, idx := range indexes {
			if idx.primary || implicit[idx.name] || constraints[idx.name].Kind == "UNIQUE" {
				continue
			}
			target := wantIndexes[idx.name]
			if target == nil || normalize(idx.definition) != normalize(indexDefinition(target)) {
				p.Changes = append(p.Changes, Change{Action: "drop_index", Table: name, Name: idx.name, Before: idx.definition, Risk: idx.unique})
			}
		}
		for _, idx := range wanted {
			if !slices.ContainsFunc(indexes, func(old index) bool {
				return old.name == idx.Name && normalize(old.definition) == normalize(indexDefinition(idx))
			}) {
				p.Changes = append(p.Changes, Change{Action: "add_index", Table: name, Name: idx.Name, After: indexDefinition(idx), Risk: idx.Class == "UNIQUE"})
			}
		}
		if db.Dialector.Name() != "sqlite" {
			info, err := tx.Migrator().TableType(table.Model)
			if err != nil {
				return nil, err
			}
			comment, _ := info.Comment()
			if comment != table.Comment {
				p.Changes = append(p.Changes, Change{Action: "table_comment", Table: name, Before: comment, After: table.Comment})
			}
		}
	}
	// No managed-table registry: every table absent from the complete target is removed.
	for _, name := range actual {
		if _, ok := d.Tables[name]; ok || !selected(name) {
			continue
		}
		keep := false
		for _, pattern := range d.Keep {
			if match, _ := filepath.Match(pattern, name); match {
				keep = true
				break
			}
		}
		if !keep {
			constraints, err := readConstraints(db.Table(name), name)
			if err != nil {
				return nil, err
			}
			keys := []string{}
			for key, c := range constraints {
				if c.Kind == "FOREIGN KEY" {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			for _, key := range keys {
				c := constraints[key]
				p.Changes = append(p.Changes, Change{Action: "drop_constraint", Table: name, Name: c.Name, Before: c.Definition, Risk: true})
			}
			p.Changes = append(p.Changes, Change{Action: "drop_table", Table: name, Risk: true})
		}
	}
	// Suspend retained foreign keys only when their columns or supporting keys change.
	for _, fk := range foreignKeys {
		reference := normalize(fk.Definition)
		reference = reference[strings.Index(reference, "references")+len("references"):]
		parent := reference[:strings.IndexByte(reference, '(')]
		for _, c := range p.Changes {
			var fields []string
			if c.Table == fk.table {
				fields = sqlColumns(fk.Definition)
			} else if c.Table == parent {
				fields = sqlColumns(reference)
			} else {
				continue
			}
			columnChange := (c.Action == "alter_column" || c.Action == "drop_column") && slices.Contains(fields, c.Name)
			keyChange := (c.Action == "drop_index" || c.Action == "drop_constraint" && (strings.HasPrefix(c.Before, "PRIMARY KEY") || strings.HasPrefix(c.Before, "UNIQUE"))) && indexCovers(c.Before, fields)
			if columnChange || keyChange {
				p.Changes = append(p.Changes,
					Change{Action: "drop_constraint", Table: fk.table, Name: fk.Name, Before: fk.Definition, Risk: true},
					Change{Action: "add_constraint", Table: fk.table, Name: fk.Name, After: fk.Definition, Risk: true})
				break
			}
		}
	}
	return p, nil
}

// Apply executes the reviewed plan. On failure it returns the precise operation.
// MySQL DDL may already have committed earlier operations; recheck before retrying.
func (d *Definition) Apply(ctx context.Context, db *gorm.DB, plan *Plan) error {
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	// SQLite needs one rebuild per changed table to retain all indexes and constraints.
	if db.Dialector.Name() == "sqlite" {
		return d.applySQLite(db, plan)
	}
	changes := slices.Clone(plan.Changes)
	priority := map[string]int{"drop_constraint": 0, "drop_index": 1, "create_table": 2, "drop_column": 3, "alter_column": 4, "add_column": 5, "add_index": 6, "add_constraint": 7, "table_comment": 8, "drop_table": 9}
	slices.SortStableFunc(changes, func(a, b Change) int { return priority[a.Action] - priority[b.Action] })
	for _, c := range changes {
		table := d.Tables[c.Table]
		tx := db.Table(c.Table)
		m := tx.Migrator()
		var err error
		switch c.Action {
		case "create_table":
			err = tx.Set("gorm:table_options", database.TableOptions(db.Dialector.Name(), "")).Migrator().CreateTable(table.Model)
			if err == nil {
				err = setComment(db, c.Table, table.Comment)
			}
		case "drop_table":
			err = m.DropTable(c.Table)
		case "add_column":
			err = m.AddColumn(table.Model, c.Name)
		case "alter_column":
			err = m.AlterColumn(table.Model, c.Name)
			if err == nil && db.Dialector.Name() == "postgres" {
				s := &gorm.Statement{DB: tx}
				err = s.Parse(table.Model)
				if err == nil {
					f := s.Schema.FieldsByDBName[c.Name]
					err = db.Exec("COMMENT ON COLUMN ?.? IS ?", clause.Table{Name: c.Table}, clause.Column{Name: c.Name}, gorm.Expr(db.Dialector.Explain("$1", f.Comment))).Error
				}
			}
		case "drop_column":
			err = m.DropColumn(table.Model, c.Name)
		case "drop_index":
			err = m.DropIndex(table.Model, c.Name)
		case "add_index":
			err = m.CreateIndex(table.Model, c.Name)
		case "drop_constraint":
			operation := "DROP CONSTRAINT"
			if db.Dialector.Name() == "mysql" {
				switch {
				case strings.HasPrefix(c.Before, "UNIQUE"):
					operation = "DROP INDEX"
				case strings.HasPrefix(c.Before, "PRIMARY KEY"):
					operation = "DROP PRIMARY KEY"
				case strings.HasPrefix(c.Before, "FOREIGN KEY"):
					operation = "DROP FOREIGN KEY"
				case strings.HasPrefix(c.Before, "CHECK"):
					var version string
					if e := db.Raw("SELECT VERSION()").Scan(&version).Error; e != nil {
						return e
					}
					if !strings.Contains(version, "MariaDB") {
						operation = "DROP CHECK"
					}
				}
			}
			query := "ALTER TABLE ? " + operation
			args := []any{clause.Table{Name: c.Table}}
			if operation != "DROP PRIMARY KEY" {
				query += " ?"
				args = append(args, clause.Column{Name: c.Name})
			}
			err = db.Exec(query, args...).Error
		case "add_constraint":
			if strings.HasPrefix(c.After, "PRIMARY KEY") {
				err = db.Exec("ALTER TABLE ? ADD "+c.After, clause.Table{Name: c.Table}).Error
			} else {
				err = m.CreateConstraint(table.Model, c.Name)
			}
		case "table_comment":
			err = setComment(db, c.Table, c.After)
		}
		if err != nil {
			return i18n.Errorf("结构同步失败%s.%s（%s）：%w", c.Table, c.Name, c.Action, err)
		}
	}
	return nil
}

func setComment(db *gorm.DB, name, comment string) error {
	switch db.Dialector.Name() {
	case "mysql":
		return db.Exec("ALTER TABLE ? COMMENT = ?", clause.Table{Name: name}, gorm.Expr(db.Dialector.Explain("?", comment))).Error
	case "postgres":
		return db.Exec("COMMENT ON TABLE ? IS ?", clause.Table{Name: name}, gorm.Expr(db.Dialector.Explain("$1", comment))).Error
	}
	return nil
}

func indexDefinition(idx *gs.Index) string {
	fields := []string{}
	for _, f := range idx.Fields {
		s := f.DBName
		if f.Expression != "" {
			s = f.Expression
		}
		if f.Length > 0 {
			s += fmt.Sprintf("(%d)", f.Length)
		}
		if f.Collate != "" {
			s += " COLLATE " + f.Collate
		}
		if f.Sort != "" && !strings.EqualFold(f.Sort, "ASC") {
			s += " " + strings.ToUpper(f.Sort)
		}
		fields = append(fields, s)
	}
	class := strings.ToUpper(idx.Class)
	if class == "" {
		class = "INDEX"
	}
	result := class + " (" + strings.Join(fields, ",") + ")"
	if idx.Type != "" && !strings.EqualFold(idx.Type, "btree") {
		result += " USING " + strings.ToLower(idx.Type)
	}
	if idx.Where != "" {
		result += " WHERE " + idx.Where
	}
	return result
}
