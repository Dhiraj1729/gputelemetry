package streamer

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func csvFile(t *testing.T, rows [][]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "metrics.csv")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	w.WriteAll(rows)
	if err = w.Error(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}
func sampleRow() []string {
	return []string{"2025-07-18T20:42:34Z", "GPU_UTIL", "0", "nvidia0", "GPU-1", "H100", "host1", "", "", "", "42.5", `host="a,b",label="quoted"`}
}
func TestCSVQuotingAndReplay(t *testing.T) {
	p := csvFile(t, [][]string{headers, sampleRow()})
	s, err := OpenCSV(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := uint64(0); i < 3; i++ {
		r, err := s.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.ReplayIndex != i || r.RowIndex != 1 || r.Measurement.Value != 42.5 || r.Measurement.LabelsRaw != sampleRow()[11] || r.Measurement.GPUUUID != "GPU-1" {
			t.Fatal(r)
		}
	}
}
func TestCSVInvalidRowsContinue(t *testing.T) {
	bad := sampleRow()
	bad[10] = "NaN"
	p := csvFile(t, [][]string{headers, {"short"}, bad, sampleRow()})
	s, err := OpenCSV(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		_, err = s.Next(context.Background())
		var invalid *InvalidRow
		if !errors.As(err, &invalid) {
			t.Fatal(err)
		}
	}
	r, err := s.Next(context.Background())
	if err != nil || r.RowIndex != 3 {
		t.Fatal(r, err)
	}
}
func TestCSVStartupAndEmpty(t *testing.T) {
	if _, err := OpenCSV(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file")
	}
	for _, h := range [][]string{{"value"}, append(append([]string{}, headers...), "value")} {
		if s, err := OpenCSV(csvFile(t, [][]string{h})); err == nil {
			s.Close()
			t.Fatal("bad header")
		}
	}
	for _, rows := range [][][]string{{headers}, {headers, {"invalid"}}} {
		s, err := OpenCSV(csvFile(t, rows))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Next(context.Background())
		var invalid *InvalidRow
		if errors.As(err, &invalid) {
			_, err = s.Next(context.Background())
		}
		if !errors.Is(err, ErrNoValidRecords) {
			t.Fatal(err)
		}
		s.Close()
	}
}
func TestMalformedQuotesAndCancelledRead(t *testing.T) {
	p := csvFile(t, [][]string{headers})
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("broken\"quote\n")
	f.Close()
	s, err := OpenCSV(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Next(context.Background())
	var invalid *InvalidRow
	if !errors.As(err, &invalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Next(ctx); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestOversizedRowSkipped(t *testing.T) {
	huge := sampleRow()
	huge[11] = strings.Repeat("x", 61<<10)
	s, err := OpenCSV(csvFile(t, [][]string{headers, huge, sampleRow()}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Next(context.Background())
	var invalid *InvalidRow
	if !errors.As(err, &invalid) {
		t.Fatal(err)
	}
	row, err := s.Next(context.Background())
	if err != nil || row.RowIndex != 2 {
		t.Fatal(row, err)
	}
}
