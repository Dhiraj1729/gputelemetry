// Package migrations embeds additive, versioned SQL. Migrations run explicitly, never in collectors.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

const Version = "001_initial.sql"

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize migration runners only, never collector inserts.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(724091203)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS gpu_schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	sql, err := files.ReadFile(Version)
	if err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(sql))
	var old string
	err = tx.QueryRow(ctx, `SELECT checksum FROM gpu_schema_migrations WHERE version=$1`, Version).Scan(&old)
	if err == nil {
		if old != checksum {
			return fmt.Errorf("migration checksum mismatch for %s", Version)
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err = tx.Exec(ctx, string(sql)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gpu_schema_migrations(version,checksum) VALUES($1,$2)`, Version, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
