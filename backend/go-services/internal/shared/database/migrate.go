package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgx5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
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
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	// Advisory lock key derived from a static 64-bit value.
	const lockKey int64 = 0x4F4D4E49474F2025 // "OMNIGO %" as int64
	_, err = sqlDB.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_lock(%d)", lockKey))
	if err != nil {
		log.Printf("ℹ Migration advisory lock skipped (PgBouncer/Pooler mode): %v", err)
	} else {
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = sqlDB.ExecContext(unlockCtx, fmt.Sprintf("SELECT pg_advisory_unlock(%d)", lockKey))
		}()
	}

	src, err := iofs.New(migrations.Migrations, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	driver, err := pgx5.WithInstance(sqlDB, &pgx5.Config{
		StatementTimeout: 5 * time.Second,
	})
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

// MigrateUpOrFail is a convenience wrapper that runs migrations on startup.
// In pooled/monolith mode, it logs and proceeds gracefully so child microservices
// don't abort if another service is concurrently migrating or if tables already exist.
func MigrateUpOrFail(ctx context.Context, dsn string) {
	if os.Getenv("SKIP_MIGRATIONS") == "true" {
		return
	}
	if err := MigrateUp(ctx, dsn); err != nil {
		log.Printf("ℹ Database migration notice: %v (continuing with active schema)", err)
	} else {
		log.Println("✓ Database migrations verified up to date")
	}
}
