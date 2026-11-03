package seeders

import (
	sdk "github.com/dawnim/dawn-server/database/seeders"
	"github.com/prismgo/framework/database"
	"gorm.io/gorm"
)

func init()                  { database.RegisterSeeder(Seed) }
func Seed(db *gorm.DB) error { return sdk.Seed(db) }
