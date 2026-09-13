package streamer

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"gpu-telemetry/internal/telemetry"
)

var ErrNoValidRecords = errors.New("CSV contains no valid records; refusing to spin through an empty replay")
var headers = []string{"timestamp", "metric_name", "gpu_id", "device", "uuid", "modelName", "Hostname", "container", "pod", "namespace", "value", "labels_raw"}

type Row struct {
	Measurement           telemetry.Measurement
	ReplayIndex, RowIndex uint64
}
type InvalidRow struct {
	ReplayIndex, RowIndex uint64
	Err                   error
}

func (e *InvalidRow) Error() string {
	return fmt.Sprintf("replay %d row %d: %v", e.ReplayIndex, e.RowIndex, e.Err)
}

type CSV struct {
	file               *os.File
	reader             *csv.Reader
	columns            map[string]int
	replay, row, valid uint64
}

func OpenCSV(path string) (*CSV, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CSV: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		f.Close()
		return nil, errors.New("CSV must be a regular file of at most 64 MiB")
	}
	s := &CSV{file: f}
	if err = s.reset(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}
func (s *CSV) Close() error { return s.file.Close() }
func (s *CSV) reset() error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	s.reader = csv.NewReader(s.file)
	h, err := s.reader.Read()
	if err != nil {
		return fmt.Errorf("read CSV header: %w", err)
	}
	s.columns = make(map[string]int, len(h))
	for i, name := range h {
		if _, ok := s.columns[name]; ok {
			return fmt.Errorf("duplicate CSV header %q", name)
		}
		s.columns[name] = i
	}
	for _, name := range headers {
		if _, ok := s.columns[name]; !ok {
			return fmt.Errorf("missing required CSV header %q", name)
		}
	}
	return nil
}

// Next reports malformed records to the caller and resumes on its next call.
// Indexes count CSV records (not physical lines), excluding each pass's header.
func (s *CSV) Next(ctx context.Context) (Row, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Row{}, err
		}
		record, err := s.reader.Read()
		if err == io.EOF {
			if s.valid == 0 {
				return Row{}, ErrNoValidRecords
			}
			s.replay++
			s.row = 0
			s.valid = 0
			if err = s.reset(); err != nil {
				return Row{}, err
			}
			continue
		}
		s.row++
		if err != nil {
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				return Row{}, &InvalidRow{s.replay, s.row, err}
			}
			return Row{}, err
		}
		get := func(k string) string { return record[s.columns[k]] }
		ts, err := time.Parse(time.RFC3339Nano, get("timestamp"))
		if err != nil {
			return Row{}, &InvalidRow{s.replay, s.row, fmt.Errorf("timestamp: %w", err)}
		}
		value, err := strconv.ParseFloat(get("value"), 64)
		if err != nil {
			return Row{}, &InvalidRow{s.replay, s.row, fmt.Errorf("value: %w", err)}
		}
		m := telemetry.Measurement{SourceTimestamp: ts.UTC(), GPUUUID: get("uuid"), MetricName: get("metric_name"), Value: value, LocalGPUID: get("gpu_id"), Device: get("device"), ModelName: get("modelName"), Hostname: get("Hostname"), Container: get("container"), Pod: get("pod"), Namespace: get("namespace"), LabelsRaw: get("labels_raw")}
		if err = m.Validate(); err != nil {
			return Row{}, &InvalidRow{s.replay, s.row, err}
		}
		encoded, err := json.Marshal(m)
		if err != nil || len(encoded) > 60<<10 {
			return Row{}, &InvalidRow{s.replay, s.row, errors.New("measurement exceeds 60 KiB JSON limit")}
		}
		s.valid++
		return Row{m, s.replay, s.row}, nil
	}
}
