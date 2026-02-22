package engine

import (
	"fmt"

	"github.com/1103020472/metatube-sdk-go/database"
	"github.com/1103020472/metatube-sdk-go/model"
)

func (e *Engine) DBAutoMigrate(v bool) error {
	if !v {
		return nil
	}
	// Create Case-Insensitive Collation for Postgres.
	if e.DBDriver() == database.Postgres {
		e.db.Exec(`CREATE COLLATION IF NOT EXISTS NOCASE (
		provider = icu,
		locale = 'und-u-ks-level2',
		deterministic = FALSE)`)
	}
	return e.db.AutoMigrate(
		&model.MovieInfo{},
		&model.ActorInfo{},
		&model.MovieReviewInfo{},
	)
}

func (e *Engine) DBDriver() string {
	return e.db.Name()
}

func (e *Engine) DBVersion() (version string, err error) {
	switch dbType := e.DBDriver(); dbType {
	case database.Postgres:
		err = e.db.Raw("SELECT version();").Scan(&version).Error
	case database.Sqlite:
		err = e.db.Raw("SELECT sqlite_version();").Scan(&version).Error
	default:
		err = fmt.Errorf("unsupported DB type: %s", dbType)
	}
	return
}
