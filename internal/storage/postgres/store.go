// Package postgres implements a shared, transactional and idempotent telemetry sink.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"gpu-telemetry/internal/telemetry"
	"gpu-telemetry/migrations"
	"strings"
	"time"
	"unicode/utf8"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string, max int32) (*Store, error) {
	return open(ctx, url, max, false)
}

// OpenReadOnly sets read-only transactions for every connection in the API pool.
func OpenReadOnly(ctx context.Context, url string, max int32) (*Store, error) {
	return open(ctx, url, max, true)
}
func open(ctx context.Context, url string, max int32, readOnly bool) (*Store, error) {
	if max < 1 || max > 10 {
		return nil, errors.New("database pool must have 1..10 connections")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	cfg.MaxConns = max
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	if readOnly {
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["application_name"] = "gpu-telemetry"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL pool")
	}
	return &Store{pool}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Ready(ctx context.Context) error {
	var version string
	return s.Pool.QueryRow(ctx, `SELECT version FROM gpu_schema_migrations WHERE version=$1`, migrations.Version).Scan(&version)
}

// Validate rejects values PostgreSQL text/timestamp columns cannot represent.
// JSON validation already happened in the transport; semantic validation belongs here too.
func Validate(e telemetry.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	for _, v := range []string{e.EventID, e.ProducerInstanceID, e.GPUUUID, e.MetricName, e.LocalGPUID, e.Device, e.ModelName, e.Hostname, e.Container, e.Pod, e.Namespace, e.LabelsRaw} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return errors.New("event contains invalid UTF-8 or NUL text")
		}
	}
	for _, v := range []time.Time{e.ProcessedAt, e.SourceTimestamp} {
		if v.Year() < 1 || v.Year() > 9999 {
			return errors.New("timestamp year must be 1..9999")
		}
	}
	return nil
}

// Persist returns only after COMMIT succeeds. An uncertain commit is an error:
// the caller leaves the message unacknowledged and a retry checks event_id again.
func (s *Store) Persist(ctx context.Context, e telemetry.Event) error {
	if err := Validate(e); err != nil {
		return err
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Deferred foreign key allows telemetry identity to win before touching metadata.
	// Duplicate deliveries therefore change neither metadata nor ingested_at.
	result, err := tx.Exec(ctx, `INSERT INTO telemetry(event_id,gpu_uuid,metric_name,value,processed_at,source_timestamp,labels_raw,container,pod,namespace,event)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(event_id) DO NOTHING`, e.EventID, e.GPUUUID, e.MetricName, e.Value, e.ProcessedAt.UTC(), e.SourceTimestamp.UTC(), e.LabelsRaw, e.Container, e.Pod, e.Namespace, payload)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		_, err = tx.Exec(ctx, `INSERT INTO gpus(uuid,hostname,local_gpu_id,device,model_name,first_seen,last_seen,last_event_id)
  VALUES($1,$2,$3,$4,$5,$6,$6,$7) ON CONFLICT(uuid) DO UPDATE SET
  first_seen=LEAST(gpus.first_seen,EXCLUDED.first_seen),
  last_seen=GREATEST(gpus.last_seen,EXCLUDED.last_seen),
  hostname=CASE WHEN (EXCLUDED.last_seen,EXCLUDED.last_event_id)>(gpus.last_seen,gpus.last_event_id) THEN EXCLUDED.hostname ELSE gpus.hostname END,
  local_gpu_id=CASE WHEN (EXCLUDED.last_seen,EXCLUDED.last_event_id)>(gpus.last_seen,gpus.last_event_id) THEN EXCLUDED.local_gpu_id ELSE gpus.local_gpu_id END,
  device=CASE WHEN (EXCLUDED.last_seen,EXCLUDED.last_event_id)>(gpus.last_seen,gpus.last_event_id) THEN EXCLUDED.device ELSE gpus.device END,
  model_name=CASE WHEN (EXCLUDED.last_seen,EXCLUDED.last_event_id)>(gpus.last_seen,gpus.last_event_id) THEN EXCLUDED.model_name ELSE gpus.model_name END,
  last_event_id=CASE WHEN (EXCLUDED.last_seen,EXCLUDED.last_event_id)>(gpus.last_seen,gpus.last_event_id) THEN EXCLUDED.last_event_id ELSE gpus.last_event_id END`, e.GPUUUID, e.Hostname, e.LocalGPUID, e.Device, e.ModelName, e.ProcessedAt.UTC(), e.EventID)
		if err != nil {
			return fmt.Errorf("GPU metadata transaction: %w", err)
		}
	}
	return tx.Commit(ctx)
}
