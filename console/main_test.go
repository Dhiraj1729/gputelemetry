package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDeployContract(t *testing.T) {
	root := t.TempDir()
	fixture := "build docker-build minikube-load helm-check:\n\t@echo STEP:$@ TAG:$(IMAGE_TAG)\nhelm-install:\n\t@echo STEP:$@ TAG:$(IMAGE_TAG)\n\t@cat \"$(VALUES_FILE)\"\n"
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	s := &server{root: root}
	if err := s.execute(context.Background(), request{Action: "build"}); err != nil {
		t.Fatal(err)
	}
	if savedTag(root) != s.tag {
		t.Fatal("successful build tag was not saved")
	}
	if err := s.execute(context.Background(), request{Action: "deploy", Streamers: 2, Collectors: 3}); err != nil {
		t.Fatal(err)
	}
	cursor := 0
	for _, target := range []string{"build", "docker-build", "minikube-load", "helm-check", "helm-install"} {
		idx := strings.Index(s.log[cursor:], "STEP:"+target+" TAG:"+s.tag)
		if idx < 0 {
			t.Fatalf("missing ordered target %s: %s", target, s.log)
		}
		cursor += idx + len("STEP:"+target)
	}
	if !strings.Contains(s.log, `"streamer":{"replicas":2}`) || !strings.Contains(s.log, `"collector":{"replicas":3}`) {
		t.Fatal("overrides not passed to installer")
	}
	if strings.Contains(s.log, `"api":`) {
		t.Fatal("console must not override API replicas")
	}
}

func TestControlAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, token, origin, host string
		want                      int
	}{
		{"valid", "secret", "http://127.0.0.1:7777", "127.0.0.1:7777", 200},
		{"missing token", "", "", "127.0.0.1:7777", 403},
		{"cross origin", "secret", "https://untrusted.example", "127.0.0.1:7777", 403},
		{"wrong host", "secret", "", "untrusted.example", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{token: "secret", host: "127.0.0.1:7777"}
			r := httptest.NewRequest("GET", "http://"+tc.host+"/control/status", nil)
			r.Header.Set("X-Console-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.api(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}
func TestRejectUnsafeOperations(t *testing.T) {
	for _, body := range []string{`{"action":"rm -rf"}`, `{"action":"deploy","streamers":11}`, `{"action":"deploy","collectors":11}`, `{"action":"check","root":"/missing/repository"}`} {
		s := &server{token: "secret", host: "127.0.0.1:7777"}
		r := httptest.NewRequest("POST", "http://127.0.0.1:7777/control/action", strings.NewReader(body))
		r.Header.Set("X-Console-Token", "secret")
		w := httptest.NewRecorder()
		s.api(w, r)
		if w.Code != 400 {
			t.Fatalf("%s: got %d", body, w.Code)
		}
	}
}
func TestConcurrentOperationRejected(t *testing.T) {
	s := &server{token: "secret", host: "127.0.0.1:7777", phase: "running"}
	r := httptest.NewRequest("POST", "http://127.0.0.1:7777/control/action", strings.NewReader(`{"action":"cluster"}`))
	r.Header.Set("X-Console-Token", "secret")
	w := httptest.NewRecorder()
	s.api(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
func TestCancellationTerminatesCommand(t *testing.T) {
	s := &server{root: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.run(ctx, "/bin/sleep", "20"); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("command was not terminated promptly")
	}
}
func TestDeployRequiresBuiltImages(t *testing.T) {
	s := &server{}
	if err := s.execute(context.Background(), request{Action: "deploy"}); err == nil {
		t.Fatal("deployment without known images accepted")
	}
}
func TestGPUProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/gpus" {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"uuid":"gpu-1"}]`))
	}))
	defer upstream.Close()
	var port int
	for _, ch := range strings.Split(upstream.URL, ":")[2] {
		port = port*10 + int(ch-'0')
	}
	s := &server{token: "secret", host: "127.0.0.1:7777", port: port}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7777/control/telemetry", nil)
	r.Header.Set("X-Console-Token", "secret")
	w := httptest.NewRecorder()
	s.api(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "gpu-1") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
