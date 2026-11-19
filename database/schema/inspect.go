package schema

import (
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/dawnim/dawn-server/support/i18n"
	"gorm.io/gorm"
	gs "gorm.io/gorm/schema"
)

type column struct {
	typ      string
	nullable bool
	def      *string
	comment  string
	auto     bool
}
type constraint struct{ Name, Kind, Definition string }
type index struct {
	name, definition string
	primary, unique  bool
}

var inlineCheck = regexp.MustCompile(`(?i)\bCHECK\s*\(`)

var integerWidth = regexp.MustCompile(`\b(tinyint|smallint|mediumint|int|integer|bigint)\(\d+\)`)
var typeCast = regexp.MustCompile(`::(?:character varying|[a-z_][a-z_0-9]*)(?:\(\d+\))?`)

func readColumns(db *gorm.DB, s *gorm.Statement) (map[string]column, error) {
	result := map[string]column{}
	if db.Dialector.Name() == "sqlite" {
		var rows []struct {
			Name, Type  string
			Notnull, PK int
			DfltValue   sql.NullString
		}
		if err := db.Raw("SELECT * FROM pragma_table_xinfo(?)", s.Table).Scan(&rows).Error; err != nil {
			return nil, err
		}
		var ddl string
		if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", s.Table).Scan(&ddl).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			c := column{auto: r.PK > 0 && strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT"), typ: r.Type, nullable: r.Notnull == 0 && r.PK == 0}
			if r.DfltValue.Valid {
				v := r.DfltValue.String
				c.def = &v
			}
			result[r.Name] = c
		}
		return result, nil
	}
	cols, err := db.Migrator().ColumnTypes(s.Table)
	if err != nil {
		return nil, err
	}
	for _, r := range cols {
		typ, ok := r.ColumnType()
		if !ok {
			typ = r.DatabaseTypeName()
		}
		if length, ok := r.Length(); ok && length > 0 && !strings.Contains(typ, "(") && (strings.Contains(typ, "char") || strings.Contains(typ, "binary")) {
			typ += fmt.Sprintf("(%d)", length)
		}
		if precision, scale, ok := r.DecimalSize(); ok && !strings.Contains(typ, "(") && (typ == "numeric" || typ == "decimal") {
			typ += fmt.Sprintf("(%d,%d)", precision, scale)
		}
		nullable, _ := r.Nullable()
		comment, _ := r.Comment()
		auto, _ := r.AutoIncrement()
		c := column{typ: typ, nullable: nullable, comment: comment, auto: auto}
		if v, ok := r.DefaultValue(); ok && !(nullable && strings.EqualFold(v, "NULL")) {
			c.def = &v
		}
		result[r.Name()] = c
	}
	return result, nil
}

func columnDiff(db *gorm.DB, f *gs.Field, c column) (string, string) {
	target := db.Dialector.DataTypeOf(f)
	if db.Dialector.Name() == "sqlite" {
		target = strings.ReplaceAll(target, " PRIMARY KEY AUTOINCREMENT", "")
	}
	actual := c.typ
	canonical := func(value string) string {
		value = strings.ToLower(value)
		for _, pair := range [][2]string{{"character varying", "varchar"}, {"double precision", "double"}, {"int8", "bigint"}, {"bigserial", "bigint"}, {"int4", "integer"}, {"serial", "integer"}, {"bool", "boolean"}} {
			if value == pair[0] || strings.HasPrefix(value, pair[0]+"(") {
				value = pair[1] + strings.TrimPrefix(value, pair[0])
				break
			}
		}
		if db.Dialector.Name() == "mysql" {
			// AUTO_INCREMENT is compared separately from the underlying SQL type.
			value = strings.TrimSpace(strings.ReplaceAll(value, "auto_increment", ""))
			value = integerWidth.ReplaceAllString(value, "$1")
			value = strings.ReplaceAll(value, "integer", "int")
			if f.GORMDataType == gs.Bool && (value == "tinyint" || value == "boolean") {
				value = "boolean"
			}
			if target == "json" && value == "longtext" {
				value = "json"
			}
		}
		return normalize(value)
	}
	actual, target = canonical(actual), canonical(target)
	before, after := []string{actual}, []string{target}
	if !f.PrimaryKey {
		before = append(before, fmt.Sprint(" nullable=", c.nullable))
		after = append(after, fmt.Sprint(" nullable=", !f.NotNull))
		have, want := "<none>", "<none>"
		if c.def != nil {
			have = normalizeDefault(*c.def, f)
		}
		if f.HasDefaultValue && !strings.EqualFold(f.DefaultValue, "NULL") && f.DefaultValue != "(-)" {
			want = normalizeDefault(f.DefaultValue, f)
		}
		before = append(before, " default="+have)
		after = append(after, " default="+want)
	}
	if db.Dialector.Name() != "sqlite" {
		before = append(before, " comment="+c.comment)
		after = append(after, " comment="+f.Comment)
	}
	before = append(before, " auto="+strconv.FormatBool(c.auto))
	after = append(after, " auto="+strconv.FormatBool(f.AutoIncrement))
	return strings.Join(before, ""), strings.Join(after, "")
}

func normalizeDefault(value string, f *gs.Field) string {
	value = typeCast.ReplaceAllString(value, "")
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	value = strings.Trim(value, "'\"")
	if f.GORMDataType == gs.Bool {
		if value == "1" || strings.EqualFold(value, "true") {
			return "true"
		}
		if value == "0" || strings.EqualFold(value, "false") {
			return "false"
		}
	}
	return value
}

func targetConstraints(db *gorm.DB, s *gorm.Statement) map[string]constraint {
	result := map[string]constraint{}
	add := func(c gs.ConstraintInterface, kind string) {
		q, args := c.Build()
		q = db.ToSQL(func(tx *gorm.DB) *gorm.DB { return tx.Exec(q, args...) })
		q = q[strings.Index(q, kind):]
		result[c.GetName()] = constraint{c.GetName(), kind, q}
	}
	for _, c := range s.Schema.ParseCheckConstraints() {
		add(&c, "CHECK")
	}
	for _, c := range s.Schema.ParseUniqueConstraints() {
		add(&c, "UNIQUE")
	}
	if !db.DisableForeignKeyConstraintWhenMigrating && !db.IgnoreRelationshipsWhenMigrating {
		for _, rel := range s.Schema.Relationships.Relations {
			if !rel.Field.IgnoreMigration {
				if c := rel.ParseConstraint(); c != nil && c.Schema == s.Schema {
					add(c, "FOREIGN KEY")
				}
			}
		}
	}
	if len(s.Schema.PrimaryFields) > 0 {
		fields := []string{}
		for _, f := range s.Schema.PrimaryFields {
			fields = append(fields, s.Quote(f.DBName))
		}
		result["@primary"] = constraint{"PRIMARY", "PRIMARY KEY", "PRIMARY KEY (" + strings.Join(fields, ",") + ")"}
	}
	return result
}

func readConstraints(db *gorm.DB, name string) (map[string]constraint, error) {
	result := map[string]constraint{}
	add := func(c constraint) {
		key := c.Name
		if c.Kind == "PRIMARY KEY" {
			key = "@primary"
		}
		result[key] = c
	}
	switch db.Dialector.Name() {
	case "sqlite":
		var ddl string
		if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&ddl).Error; err != nil {
			return nil, err
		}
		start := strings.Index(ddl, "(")
		end := sqlGroupEnd(ddl, start)
		if start < 0 || end < 0 {
			return nil, i18n.Errorf("无法解析表结构：%s", name)
		}
		parts := splitSQL(ddl[start+1 : end])
		for i, part := range parts {
			c := constraint{Name: fmt.Sprintf("unnamed_%d", i)}
			part = strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToUpper(part), "CONSTRAINT ") {
				tail := strings.TrimSpace(part[len("CONSTRAINT "):])
				end := strings.IndexByte(tail, ' ')
				if end < 0 {
					continue
				}
				c.Name = strings.Trim(tail[:end], "`\"[]")
				part = strings.TrimSpace(tail[end:])
			}
			for _, kind := range []string{"PRIMARY KEY", "FOREIGN KEY", "CHECK", "UNIQUE"} {
				if strings.HasPrefix(strings.ToUpper(part), kind) {
					c.Kind = kind
					c.Definition = part
					add(c)
					break
				}
			}
		}
		// SQLite may store inline UNIQUE/FK/CHECK constraints without a declared name.
		for i, part := range parts {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(part)), "CONSTRAINT ") {
				continue
			}
			if found := inlineCheck.FindStringIndex(part); found != nil {
				start := found[1] - 1
				end := sqlGroupEnd(part, start)
				if end >= 0 {
					add(constraint{fmt.Sprintf("inline_check_%d", i), "CHECK", "CHECK " + part[start:end+1]})
				}
			}
		}
		var uniqueIndexes []struct{ Name string }
		if err := db.Raw("SELECT name FROM pragma_index_list(?) WHERE origin='u' ORDER BY name", name).Scan(&uniqueIndexes).Error; err != nil {
			return nil, err
		}
		for _, idx := range uniqueIndexes {
			var fields []string
			if err := db.Raw("SELECT name FROM pragma_index_info(?) ORDER BY seqno", idx.Name).Scan(&fields).Error; err != nil {
				return nil, err
			}
			def := "UNIQUE (" + strings.Join(fields, ",") + ")"
			found := false
			for _, c := range result {
				if normalize(c.Definition) == normalize(def) {
					found = true
					break
				}
			}
			if !found {
				add(constraint{idx.Name, "UNIQUE", def})
			}
		}
		var foreignKeys []struct {
			ID, Seq                             int
			Table, From, To, OnUpdate, OnDelete string
		}
		if err := db.Raw("SELECT * FROM pragma_foreign_key_list(?) ORDER BY id,seq", name).Scan(&foreignKeys).Error; err != nil {
			return nil, err
		}
		for i := 0; i < len(foreignKeys); {
			fk := foreignKeys[i]
			fields, refs := []string{}, []string{}
			for i < len(foreignKeys) && foreignKeys[i].ID == fk.ID {
				fields = append(fields, foreignKeys[i].From)
				refs = append(refs, foreignKeys[i].To)
				i++
			}
			def := "FOREIGN KEY (" + strings.Join(fields, ",") + ") REFERENCES " + fk.Table + " (" + strings.Join(refs, ",") + ") ON DELETE " + fk.OnDelete + " ON UPDATE " + fk.OnUpdate
			found := false
			for _, c := range result {
				if normalize(c.Definition) == normalize(def) {
					found = true
					break
				}
			}
			if !found {
				add(constraint{fmt.Sprintf("inline_fk_%d", fk.ID), "FOREIGN KEY", def})
			}
		}

		if _, ok := result["@primary"]; !ok {
			var cols []struct {
				Name string
				PK   int
			}
			if err := db.Raw("SELECT name,pk FROM pragma_table_info(?) WHERE pk>0 ORDER BY pk", name).Scan(&cols).Error; err != nil {
				return nil, err
			}
			if len(cols) > 0 {
				fields := []string{}
				for _, col := range cols {
					fields = append(fields, col.Name)
				}
				add(constraint{"PRIMARY", "PRIMARY KEY", "PRIMARY KEY (" + strings.Join(fields, ",") + ")"})
			}
		}
	case "postgres":
		var rows []struct{ Name, Kind, Definition string }
		if err := db.Raw(`SELECT c.conname AS name, c.contype::text AS kind, pg_get_constraintdef(c.oid) AS definition FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE t.relname=? AND n.nspname=current_schema()`, name).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			kind := map[string]string{"p": "PRIMARY KEY", "f": "FOREIGN KEY", "u": "UNIQUE", "c": "CHECK"}[r.Kind]
			if kind != "" {
				add(constraint{r.Name, kind, r.Definition})
			}
		}
	case "mysql":
		var rows []struct{ Name, Kind string }
		if err := db.Raw(`SELECT CONSTRAINT_NAME AS name,CONSTRAINT_TYPE AS kind FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY CONSTRAINT_NAME`, name).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			c := constraint{Name: r.Name, Kind: r.Kind}
			switch r.Kind {
			case "PRIMARY KEY", "UNIQUE", "FOREIGN KEY":
				var cols []struct{ ColumnName, ReferencedTableName, ReferencedColumnName string }
				if err := db.Raw(`SELECT COLUMN_NAME,COALESCE(REFERENCED_TABLE_NAME,'') AS referenced_table_name,COALESCE(REFERENCED_COLUMN_NAME,'') AS referenced_column_name FROM information_schema.KEY_COLUMN_USAGE WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME=? AND CONSTRAINT_NAME=? ORDER BY ORDINAL_POSITION`, name, r.Name).Scan(&cols).Error; err != nil {
					return nil, err
				}
				fields, refs := []string{}, []string{}
				for _, col := range cols {
					fields = append(fields, col.ColumnName)
					refs = append(refs, col.ReferencedColumnName)
				}
				c.Definition = r.Kind + " (" + strings.Join(fields, ",") + ")"
				if r.Kind == "FOREIGN KEY" && len(cols) > 0 {
					var rules struct{ UpdateRule, DeleteRule string }
					if err := db.Raw(`SELECT UPDATE_RULE,DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME=? AND CONSTRAINT_NAME=?`, name, r.Name).Scan(&rules).Error; err != nil {
						return nil, err
					}
					c.Definition += " REFERENCES " + cols[0].ReferencedTableName + " (" + strings.Join(refs, ",") + ") ON DELETE " + rules.DeleteRule + " ON UPDATE " + rules.UpdateRule
				}
			case "CHECK":
				var expr string
				if err := db.Raw(`SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?`, r.Name).Scan(&expr).Error; err != nil {
					return nil, err
				}
				// MariaDB implements the JSON data type with an automatic json_valid check.
				if strings.HasPrefix(normalize(expr), "json_valid(") {
					continue
				}
				c.Definition = "CHECK (" + expr + ")"
			}
			if c.Definition != "" {
				add(c)
			}
		}
	default:
		return nil, i18n.Errorf("暂不支持数据库类型%s", db.Dialector.Name())
	}
	return result, nil
}

// splitSQL splits table/index fields without splitting expressions or quoted values.
func splitSQL(value string) []string {
	parts := []string{}
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(value); i++ {
		b := value[i]
		if quote != 0 {
			if b == quote {
				if i+1 < len(value) && value[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			} else if b == '\\' {
				i++
			}
			continue
		}
		switch b {
		case '\'', '"', '`':
			quote = b
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(value[start:]))
}

func sqlGroupEnd(value string, start int) int {
	if start < 0 {
		return -1
	}
	depth := 0
	var quote byte
	for i := start; i < len(value); i++ {
		b := value[i]
		if quote != 0 {
			if b == quote {
				if i+1 < len(value) && value[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			} else if b == '\\' {
				i++
			}
			continue
		}
		switch b {
		case '\'', '"', '`':
			quote = b
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func normalize(value string) string {
	value = typeCast.ReplaceAllString(value, "")
	var out strings.Builder
	var quote rune
	for _, r := range value {
		if quote == '\'' {
			out.WriteRune(r)
			if r == '\'' {
				quote = 0
			}
			continue
		}
		if r == '\'' {
			quote = r
			out.WriteRune(r)
			continue
		}
		if r == '`' || r == '"' || unicode.IsSpace(r) {
			continue
		}
		out.WriteRune(unicode.ToLower(r))
	}
	value = out.String()
	value = strings.ReplaceAll(value, "onupdatenoaction", "")
	value = strings.ReplaceAll(value, "ondeletenoaction", "")
	value = strings.ReplaceAll(value, "onupdaterestrict", "")
	value = strings.ReplaceAll(value, "ondeleterestrict", "")
	if strings.HasPrefix(value, "check(") {
		expr := value[6 : len(value)-1]
		for strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") && len(splitSQL(expr[1:len(expr)-1])) == 1 {
			depth, whole := 0, true
			for i, r := range expr {
				if r == '(' {
					depth++
				}
				if r == ')' {
					depth--
				}
				if depth == 0 && i < len(expr)-1 {
					whole = false
					break
				}
			}
			if !whole {
				break
			}
			expr = expr[1 : len(expr)-1]
		}
		value = "check(" + expr + ")"
	}
	return value
}

func sqlColumns(definition string) []string {
	start := strings.IndexByte(definition, '(')
	end := sqlGroupEnd(definition, start)
	if end < 0 {
		return nil
	}
	fields := splitSQL(definition[start+1 : end])
	for i := range fields {
		fields[i] = normalize(fields[i])
	}
	return fields
}

// InnoDB foreign keys require an index with these columns as its leading fields.
func indexCovers(definition string, fields []string) bool {
	columns := sqlColumns(definition)
	return len(fields) > 0 && len(columns) >= len(fields) && slices.Equal(columns[:len(fields)], fields)
}

func readIndexes(db *gorm.DB, name string) ([]index, error) {
	result := []index{}
	switch db.Dialector.Name() {
	case "sqlite":
		var rows []struct {
			Name, Origin string
			Unique       bool
		}
		if err := db.Raw("SELECT name,origin,`unique` FROM pragma_index_list(?) ORDER BY name", name).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Origin == "pk" || r.Origin == "u" {
				continue
			}
			var ddl string
			if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", r.Name).Scan(&ddl).Error; err != nil {
				return nil, err
			}
			result = append(result, index{name: r.Name, unique: r.Unique, definition: storedIndexDefinition(ddl, r.Unique)})
		}
	case "mysql":
		var rows []struct {
			KeyName, ColumnName, Collation, IndexType string
			NonUnique                                 int
			SubPart                                   sql.NullInt64
			Expression                                sql.NullString
		}
		if err := db.Raw("SHOW INDEX FROM " + db.Statement.Quote(name)).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			pos := slices.IndexFunc(result, func(i index) bool { return i.name == r.KeyName })
			if pos < 0 {
				result = append(result, index{name: r.KeyName, unique: r.NonUnique == 0, primary: r.KeyName == "PRIMARY"})
				pos = len(result) - 1
			}
			s := r.ColumnName
			if r.Expression.Valid {
				s = r.Expression.String
			}
			if r.SubPart.Valid {
				s += fmt.Sprintf("(%d)", r.SubPart.Int64)
			}
			if r.Collation == "D" {
				s += " DESC"
			}
			if result[pos].definition != "" {
				result[pos].definition += ","
			}
			result[pos].definition += s
		}
		for i := range result {
			class := "INDEX"
			if result[i].unique {
				class = "UNIQUE"
			}
			for _, r := range rows {
				if r.KeyName == result[i].name && (r.IndexType == "FULLTEXT" || r.IndexType == "SPATIAL") {
					class = r.IndexType
				}
			}
			result[i].definition = class + " (" + result[i].definition + ")"
		}
	case "postgres":
		var rows []struct {
			Name, Definition    string
			IsPrimary, IsUnique bool
		}
		if err := db.Raw(`SELECT i.relname AS name,pg_get_indexdef(i.oid) AS definition,x.indisprimary AS is_primary,x.indisunique AS is_unique FROM pg_index x JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_class i ON i.oid=x.indexrelid WHERE t.relname=? AND n.nspname=current_schema() ORDER BY i.relname`, name).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			result = append(result, index{name: r.Name, primary: r.IsPrimary, unique: r.IsUnique, definition: storedIndexDefinition(r.Definition, r.IsUnique)})
		}
	}
	slices.SortFunc(result, func(a, b index) int { return strings.Compare(a.name, b.name) })
	return result, nil
}

func storedIndexDefinition(ddl string, unique bool) string {
	start := strings.Index(ddl, "(")
	if start < 0 {
		return ddl
	}
	class := "INDEX"
	if unique {
		class = "UNIQUE"
	}
	end := sqlGroupEnd(ddl, start)
	if end < 0 {
		return ddl
	}
	result := class + " " + ddl[start:end+1]
	if pos := strings.Index(ddl[:start], " USING "); pos >= 0 {
		typ := strings.TrimSpace(ddl[pos+7 : start])
		if typ != "btree" {
			result += " USING " + typ
		}
	}
	result += ddl[end+1:]
	result = strings.ReplaceAll(result, " ASC", "")
	return result
}
