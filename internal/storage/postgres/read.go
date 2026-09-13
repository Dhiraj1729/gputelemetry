package postgres

import (
	"context"
	"errors"
	"time"
)

var ErrGPUNotFound = errors.New("GPU not found")

type GPU struct {
	UUID       string    `json:"uuid" doc:"GPU UUID; local GPU IDs are not globally unique"`
	Hostname   string    `json:"hostname"`
	LocalGPUID string    `json:"local_gpu_id"`
	Device     string    `json:"device"`
	ModelName  string    `json:"model_name"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}
type Datapoint struct {
	EventID         string    `json:"event_id"`
	GPUUUID         string    `json:"gpu_uuid"`
	MetricName      string    `json:"metric_name"`
	Value           float64   `json:"value"`
	ProcessedAt     time.Time `json:"processed_at" doc:"Streamer-assigned timestamp used for ordering and filtering"`
	SourceTimestamp time.Time `json:"source_timestamp" doc:"Original CSV timestamp"`
	IngestedAt      time.Time `json:"ingested_at" doc:"First successful database insert time"`
	LabelsRaw       string    `json:"labels_raw"`
	Container       string    `json:"container"`
	Pod             string    `json:"pod"`
	Namespace       string    `json:"namespace"`
}

// VisitGPUs emits one row at a time. The callback must not retain all rows.
func (s *Store) VisitGPUs(ctx context.Context, visit func(GPU) error) error {
	rows, err := s.Pool.Query(ctx, `SELECT uuid,hostname,local_gpu_id,device,model_name,first_seen,last_seen FROM gpus g WHERE EXISTS(SELECT 1 FROM telemetry t WHERE t.gpu_uuid=g.uuid) ORDER BY uuid ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g GPU
		if err = rows.Scan(&g.UUID, &g.Hostname, &g.LocalGPUID, &g.Device, &g.ModelName, &g.FirstSeen, &g.LastSeen); err != nil {
			return err
		}
		g.FirstSeen = g.FirstSeen.UTC()
		g.LastSeen = g.LastSeen.UTC()
		if err = visit(g); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *Store) VisitTelemetry(ctx context.Context, id string, start, end *time.Time, visit func(Datapoint) error) error {
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gpus WHERE uuid=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrGPUNotFound
	}
	// PostgreSQL stores microseconds. Ceil the lower and floor the upper bound,
	// so sub-microsecond RFC3339 inputs cannot include a row outside the range.
	if start != nil {
		v := start.UTC().Truncate(time.Microsecond)
		if v.Before(*start) {
			v = v.Add(time.Microsecond)
		}
		start = &v
	}
	if end != nil {
		v := end.UTC().Truncate(time.Microsecond)
		end = &v
	}
	rows, err := s.Pool.Query(ctx, `SELECT event_id,gpu_uuid,metric_name,value,processed_at,source_timestamp,ingested_at,labels_raw,container,pod,namespace FROM telemetry WHERE gpu_uuid=$1 AND ($2::timestamptz IS NULL OR processed_at >= $2) AND ($3::timestamptz IS NULL OR processed_at <= $3) ORDER BY processed_at ASC,event_id ASC`, id, start, end)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d Datapoint
		if err = rows.Scan(&d.EventID, &d.GPUUUID, &d.MetricName, &d.Value, &d.ProcessedAt, &d.SourceTimestamp, &d.IngestedAt, &d.LabelsRaw, &d.Container, &d.Pod, &d.Namespace); err != nil {
			return err
		}
		d.ProcessedAt = d.ProcessedAt.UTC()
		d.SourceTimestamp = d.SourceTimestamp.UTC()
		d.IngestedAt = d.IngestedAt.UTC()
		if err = visit(d); err != nil {
			return err
		}
	}
	return rows.Err()
}
