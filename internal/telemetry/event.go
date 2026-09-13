// Package telemetry defines the versioned wire contract shared by producers and queues.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

const SchemaVersion = 1

// Measurement preserves each independent CSV metric and its source metadata.
type Measurement struct {
	SourceTimestamp time.Time `json:"source_timestamp"`
	GPUUUID         string    `json:"gpu_uuid"`
	MetricName      string    `json:"metric_name"`
	Value           float64   `json:"value"`
	LocalGPUID      string    `json:"gpu_id"`
	Device          string    `json:"device"`
	ModelName       string    `json:"model_name"`
	Hostname        string    `json:"hostname"`
	Container       string    `json:"container"`
	Pod             string    `json:"pod"`
	Namespace       string    `json:"namespace"`
	LabelsRaw       string    `json:"labels_raw"`
}

type Event struct {
	SchemaVersion      int       `json:"schema_version"`
	EventID            string    `json:"event_id"`
	ProducerInstanceID string    `json:"producer_instance_id"`
	ReplayIndex        uint64    `json:"replay_index"`
	RowIndex           uint64    `json:"row_index"`
	ProcessedAt        time.Time `json:"processed_at"`
	Measurement
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (m Measurement) Validate() error {
	if m.GPUUUID == "" || m.MetricName == "" || m.SourceTimestamp.IsZero() {
		return fmt.Errorf("gpu_uuid, metric_name and source_timestamp are required")
	}
	if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
		return fmt.Errorf("value must be finite")
	}
	return nil
}

func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version: %d", e.SchemaVersion)
	}
	if e.EventID == "" || len(e.EventID) > 128 || e.ProducerInstanceID == "" || len(e.ProducerInstanceID) > 128 {
		return fmt.Errorf("event and producer IDs must contain 1-128 bytes")
	}
	if e.RowIndex == 0 || e.ProcessedAt.IsZero() || e.ProcessedAt.Nanosecond()%1000 != 0 {
		return fmt.Errorf("row_index must be positive; processed_at must have microsecond precision")
	}
	return e.Measurement.Validate()
}
