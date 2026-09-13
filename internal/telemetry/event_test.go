package telemetry

import (
	"math"
	"testing"
	"time"
)

func TestValidation(t *testing.T) {
	good := Event{SchemaVersion: 1, EventID: "id", ProducerInstanceID: "producer", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: Measurement{SourceTimestamp: time.Now(), GPUUUID: "GPU-1", MetricName: "temperature", Value: 42}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.SchemaVersion = 2 }, func(e *Event) { e.EventID = "" }, func(e *Event) { e.GPUUUID = "" }, func(e *Event) { e.Value = math.NaN() }, func(e *Event) { e.Value = math.Inf(1) }, func(e *Event) { e.RowIndex = 0 }, func(e *Event) { e.ProcessedAt = e.ProcessedAt.Add(time.Nanosecond) }} {
		e := good
		change(&e)
		if e.Validate() == nil {
			t.Fatalf("accepted invalid event: %+v", e)
		}
	}
}
func TestIDs(t *testing.T) {
	a, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || a == b {
		t.Fatal(a, b)
	}
}
