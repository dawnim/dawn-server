package migrations

import (
	sdk "github.com/dawnim/dawn-server/database/migrations"
	"github.com/prismgo/framework/database"
	"gorm.io/gorm"
	"path/filepath"
	"runtime"
)

var _, sourceFile, _, _ = runtime.Caller(0)
var Directory = filepath.Dir(sourceFile)

func init() {
	database.RegisterMigration(func(db *gorm.DB) error { return sdk.Up(db) }, func(db *gorm.DB) error { return sdk.Down(db) })
}
