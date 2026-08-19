package database

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB encapsulates the Master-Replica connection pools
type DB struct {
	Writer *pgxpool.Pool
	Reader *pgxpool.Pool
}

// NewDB initializes the connection pools using connection strings.
// Pool sizing is env-configurable for production tuning:
//
//	DB_MAX_CONNS_WRITER (default 10), DB_MIN_CONNS_WRITER (default 2)
//	DB_MAX_CONNS_READER (default 20), DB_MIN_CONNS_READER (default 4)
//	DB_CONN_MAX_LIFETIME (default 30m), DB_CONN_MAX_IDLE_TIME (default 5m)
func NewDB(ctx context.Context, writerDSN, readerDSN string) (*DB, error) {
	maxLifetime := envDur("DB_CONN_MAX_LIFETIME", 30*time.Minute)
	maxIdleTime := envDur("DB_CONN_MAX_IDLE_TIME", 5*time.Minute)

	writerCfg, err := pgxpool.ParseConfig(writerDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse writer DSN: %w", err)
	}
	writerCfg.MaxConns = envInt32("DB_MAX_CONNS_WRITER", 10)
	writerCfg.MinConns = envInt32("DB_MIN_CONNS_WRITER", 2)
	writerCfg.MaxConnLifetime = maxLifetime
	writerCfg.MaxConnIdleTime = maxIdleTime
	writerCfg.HealthCheckPeriod = 30 * time.Second

	readerCfg, err := pgxpool.ParseConfig(readerDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reader DSN: %w", err)
	}
	readerCfg.MaxConns = envInt32("DB_MAX_CONNS_READER", 20)
	readerCfg.MinConns = envInt32("DB_MIN_CONNS_READER", 4)
	readerCfg.MaxConnLifetime = maxLifetime
	readerCfg.MaxConnIdleTime = maxIdleTime
	readerCfg.HealthCheckPeriod = 30 * time.Second

	writer, err := pgxpool.NewWithConfig(ctx, writerCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to writer: %w", err)
	}

	reader, err := pgxpool.NewWithConfig(ctx, readerCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to reader: %w", err)
	}

	return &DB{
		Writer: writer,
		Reader: reader,
	}, nil
}

// Close releases all connections
func (db *DB) Close() {
	if db.Writer != nil {
		db.Writer.Close()
	}
	if db.Reader != nil {
		db.Reader.Close()
	}
}

// rowQuerier is the subset of pgx pool/transaction types needed for EXISTS probes.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// Exists returns whether at least one row matches the provided subquery.
// The subquery should be a SELECT (e.g. "SELECT 1 FROM users WHERE tracking_id = $1").
func Exists(ctx context.Context, db rowQuerier, subquery string, args ...interface{}) (bool, error) {
	var ok bool
	query := fmt.Sprintf("SELECT EXISTS (%s)", subquery)
	err := db.QueryRow(ctx, query, args...).Scan(&ok)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return ok, err
}

func envInt32(key string, def int32) int32 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			return int32(n)
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
