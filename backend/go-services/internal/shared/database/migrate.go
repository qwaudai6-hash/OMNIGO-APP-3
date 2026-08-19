package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgx5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnigo/backend/migrations"
)

// MigrateUp runs all embedded migrations against the supplied DSN.
// It uses a PostgreSQL advisory lock to serialize concurrent startup attempts
// and fails loudly on any migration error (no silent skipping).
func MigrateUp(ctx context.Context, dsn string) error {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse migration dsn: %w", err)
	}

	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	// Advisory lock key derived from a static 64-bit value. Multiple instances
	// of the same service will contend on this lock and run migrations serially.
	const lockKey int64 = 0x4F4D4E49474F2025 // "OMNIGO %" as int64
	_, err = sqlDB.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey)
	if err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = sqlDB.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", lockKey)
	}()

	src, err := iofs.New(migrations.Migrations, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	driver, err := pgx5.WithInstance(sqlDB, &pgx5.Config{})
	if err != nil {
		return fmt.Errorf("create migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("initialize migrate: %w", err)
	}

	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			return nil
		}
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}

// MigrateUpOrFail is a convenience wrapper that logs and exits on error.
// Use it in service main.go files before starting the HTTP server.
func MigrateUpOrFail(ctx context.Context, dsn string) {
	if err := MigrateUp(ctx, dsn); err != nil {
		log.Fatalf("FATAL: database migration failed: %v", err)
	}
}
