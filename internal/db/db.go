package db

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

var (
	instance *DB
	once     sync.Once
)

// GetDB returns the singleton database instance
func GetDB() *DB {
	return instance
}

// Init initializes the database connection
func Init(cfg *config.Config) error {
	var err error
	once.Do(func() {
		instance, err = connect(cfg)
	})
	return err
}

func connect(cfg *config.Config) (*DB, error) {
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.DBUser,
		cfg.DBPassword,
		cfg.DBHost,
		cfg.DBPort,
		cfg.DBName,
	)

	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pgx config: %w", err)
	}

	config.MaxConns = int32(cfg.DBMaxConnections)
	config.MinConns = 5
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = time.Minute * 10
	config.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgx pool: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{pool: pool}, nil
}

// Close closes the database connection
func (d *DB) Close() error {
	if d.pool != nil {
		d.pool.Close()
	}
	return nil
}

// QueryRow executes a query that returns a single row
func (d *DB) QueryRow(ctx context.Context, sql string, args ...interface{}) interface{} {
	return d.pool.QueryRow(ctx, sql, args...)
}

// Query executes a query that returns multiple rows
func (d *DB) Query(ctx context.Context, sql string, args ...interface{}) interface{} {
	rows, _ := d.pool.Query(ctx, sql, args...)
	return rows
}

// Exec executes a command
// Exec executes a command
func (d *DB) Exec(ctx context.Context, sql string, args ...interface{}) error {
	_, err := d.pool.Exec(ctx, sql, args...)
	return err
}

// GetPool returns the underlying connection pool
func (d *DB) GetPool() *pgxpool.Pool {
	return d.pool
}
