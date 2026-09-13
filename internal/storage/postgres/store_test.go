package postgres

import (
	"context"
	"gpu-telemetry/internal/telemetry"
	"testing"
	"time"
)

func TestValidation(t *testing.T) {
	e := telemetry.Event{SchemaVersion: 1, EventID: "id", ProducerInstanceID: "p", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "g", MetricName: "m", SourceTimestamp: time.Now()}}
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"bad\x00text", "bad\xff"} {
		e.LabelsRaw = v
		if Validate(e) == nil {
			t.Fatal("invalid text accepted")
		}
	}
	e.LabelsRaw = ""
	e.SourceTimestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if Validate(e) == nil {
		t.Fatal("invalid time")
	}
	e.SchemaVersion = 2
	if Validate(e) == nil {
		t.Fatal("invalid schema")
	}
	if err := (&Store{}).Persist(context.Background(), e); err == nil {
		t.Fatal("invalid stored")
	}
}
func TestOpenConfiguration(t *testing.T) {
	if _, err := Open(context.Background(), "", 0); err == nil {
		t.Fatal("invalid pool")
	}
	if _, err := Open(context.Background(), "://bad", 2); err == nil {
		t.Fatal("invalid URL")
	}
}
