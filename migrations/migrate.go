package migrations

import (
	"database/sql"
	"embed"
	"errors"

	driver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var files embed.FS

// Up uses golang-migrate's version table, dirty-state detection and database lock.
// The migration connection is separate from the application's connection pool.
func Up(dsn string) error {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return errors.New("konfigurasi DSN migrasi tidak valid")
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	database, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		return err
	}
	source, err := iofs.New(files, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", source, cfg.DBName, database)
	if err != nil {
		_ = source.Close()
		return err
	}
	defer m.Close()
	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
