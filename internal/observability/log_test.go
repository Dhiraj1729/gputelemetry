package observability

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStructuredLog(t *testing.T) {
	var b bytes.Buffer
	l, err := New(&b, "info")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("accepted", "count", 1)
	var v map[string]any
	if err = json.Unmarshal(b.Bytes(), &v); err != nil || v["msg"] != "accepted" || v["count"] != float64(1) {
		t.Fatal(v, err)
	}
	if _, err = New(&b, "bad"); err == nil {
		t.Fatal("bad log level")
	}
}
