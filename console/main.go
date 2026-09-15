package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type server struct {
	mu                                         sync.Mutex
	root, token, host, tag, log, action, phase string
	results                                    map[string]string
	cancel                                     context.CancelFunc
	forward                                    *exec.Cmd
	port                                       int
}
type request struct {
	Confirmation string `json:"confirmation"`
	Action       string `json:"action"`
	Root         string `json:"root"`
	Streamers    int    `json:"streamers"`
	Collectors   int    `json:"collectors"`
}

func savedTag(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "work", "console-image-tag"))
	tag := strings.TrimSpace(string(data))
	if err == nil && regexp.MustCompile(`^console-[0-9]{8}-[0-9]{6}$`).MatchString(tag) {
		return tag
	}
	return ""
}

func validRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("select an absolute repository path")
	}
	for _, p := range []string{"Makefile", "Brewfile", "go.mod", "scripts/install-tools.sh", "scripts/cluster-up.sh", "scripts/images.sh", "scripts/helm-install.sh", "scripts/helm-verify.sh", "deploy/docker/Dockerfile", "deploy/helm/gpu-telemetry/values.yaml", "Problemstatement/dcgm_metrics_20250718_134233.csv"} {
		st, err := os.Stat(filepath.Join(root, p))
		if err != nil || st.IsDir() {
			return fmt.Errorf("repository is missing %s", p)
		}
	}
	return nil
}
func (s *server) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log += string(p)
	if len(s.log) > 160000 {
		s.log = s.log[len(s.log)-160000:]
	}
	return len(p), nil
}
func (s *server) run(ctx context.Context, name string, args ...string) error {
	return s.command(ctx, s, name, args...)
}
func (s *server) capture(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	err := s.command(ctx, &out, name, args...)
	return out.String(), err
}
func (s *server) command(ctx context.Context, output io.Writer, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(s, "\n$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = s.root
	cmd.Env = append(os.Environ(), "PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:/usr/sbin:/sbin", "IMAGE_TAG="+s.tag, "DOCKER_CONTEXT=colima-gpu-telemetry", "MINIKUBE_PROFILE=gpu-telemetry", "KUBE_CONTEXT=gpu-telemetry", "RELEASE=gpu-telemetry", "NAMESPACE=gpu-telemetry")
	cmd.Stdout = output
	cmd.Stderr = s
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}
func (s *server) stopForward() {
	s.mu.Lock()
	cmd := s.forward
	s.forward = nil
	s.port = 0
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func (s *server) connect(ctx context.Context) error {
	s.stopForward()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("/opt/homebrew/bin/kubectl", "--context", "gpu-telemetry", "-n", "gpu-telemetry", "port-forward", "--address=127.0.0.1", "service/gpu-telemetry-api", fmt.Sprintf("%d:8080", port))
	cmd.Stdout = s
	cmd.Stderr = s
	if err = cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	s.forward = cmd
	s.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		if s.forward == cmd {
			s.forward = nil
			s.port = 0
		}
		s.mu.Unlock()
	}()
	client := http.Client{Timeout: time.Second}
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			s.stopForward()
			return ctx.Err()
		case <-time.After(time.Second):
		}
		res, e := client.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", port))
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				s.mu.Lock()
				s.port = port
				s.mu.Unlock()
				return nil
			}
		}
	}
	s.stopForward()
	return fmt.Errorf("API did not become ready within 60 seconds")
}
func (s *server) execute(ctx context.Context, r request) error {
	switch r.Action {
	case "check":
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
			return fmt.Errorf("this console currently supports Apple Silicon macOS")
		}
		if err := validRoot(s.root); err != nil {
			return err
		}
		if err := s.run(ctx, "/usr/bin/xcode-select", "-p"); err != nil {
			return fmt.Errorf("install Apple Command Line Tools in Terminal: xcode-select --install")
		}
		if err := s.run(ctx, "/opt/homebrew/bin/brew", "--version"); err != nil {
			return fmt.Errorf("install Homebrew from https://brew.sh, then check again")
		}
		return nil
	case "tools":
		if err := s.run(ctx, "/usr/bin/make", "tools"); err != nil {
			return err
		}
		return s.run(ctx, "/usr/bin/make", "doctor")
	case "cluster":
		return s.run(ctx, "/usr/bin/make", "cluster-up")
	case "build":
		s.tag = "console-" + time.Now().UTC().Format("20060102-150405")
		for _, target := range []string{"build", "docker-build", "minikube-load"} {
			if err := s.run(ctx, "/usr/bin/make", target); err != nil {
				s.tag = savedTag(s.root)
				return err
			}
		}
		if err := os.MkdirAll(filepath.Join(s.root, "work"), 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(s.root, "work", "console-image-tag"), []byte(s.tag), 0600)
	case "deploy":
		if s.tag == "" {
			return fmt.Errorf("build and load images through the console before deploying")
		}
		if err := s.run(ctx, "/usr/bin/make", "helm-check"); err != nil {
			return err
		}
		args := []string{"helm-install"}
		if r.Streamers != 0 || r.Collectors != 0 {
			values := map[string]any{}
			for k, v := range map[string]int{"streamer": r.Streamers, "collector": r.Collectors} {
				if v > 0 {
					values[k] = map[string]int{"replicas": v}
				}
			}
			f, e := os.CreateTemp("", "telemetry-console-values-*.json")
			if e != nil {
				return e
			}
			defer os.Remove(f.Name())
			if e = json.NewEncoder(f).Encode(values); e != nil {
				f.Close()
				return e
			}
			f.Close()
			args = append(args, "VALUES_FILE="+f.Name())
		}
		return s.run(ctx, "/usr/bin/make", args...)
	case "verify":
		return s.run(ctx, "/usr/bin/make", "helm-verify")
	case "connect":
		return s.connect(ctx)
	case "disconnect":
		s.stopForward()
		return nil
	case "stop":
		s.stopForward()
		return s.run(ctx, "/usr/bin/make", "cluster-down")
	case "cleanup", "remove-environment":
		if r.Confirmation != cleanupConfirmation(r.Action) {
			return fmt.Errorf("cleanup confirmation required")
		}
		s.stopForward()
		if err := s.run(ctx, "/usr/bin/make", "cluster-up"); err != nil {
			return err
		}
		c := cleaner{call: s.capture, log: s}
		if err := c.clean(ctx, r.Action == "remove-environment"); err != nil {
			return fmt.Errorf("cleanup stopped; environment left available for investigation: %w", err)
		}
		if r.Action == "remove-environment" {
			if err := os.Remove(filepath.Join(s.root, "work", "console-image-tag")); err != nil && !os.IsNotExist(err) {
				return err
			}
			s.tag = ""
		} else if err := s.run(ctx, "/usr/bin/make", "cluster-down"); err != nil {
			return err
		}
		s.mu.Lock()
		s.results = map[string]string{}
		s.mu.Unlock()
		fmt.Fprintln(s, "Deployment data removed. Cleanup and shutdown verified.")
		return nil
	case "status":
		return s.run(ctx, "/opt/homebrew/bin/kubectl", "--context", "gpu-telemetry", "-n", "gpu-telemetry", "get", "pods,jobs,services,pvc")
	}
	return fmt.Errorf("unknown action")
}
func (s *server) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Host != s.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+s.host) || r.Header.Get("X-Console-Token") != s.token {
		http.Error(w, "forbidden", 403)
		return
	}
	if r.URL.Path == "/control/status" && r.Method == "GET" {
		s.mu.Lock()
		defer s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"root": s.root, "action": s.action, "phase": s.phase, "log": s.log, "results": s.results, "connected": s.port > 0})
		return
	}
	if r.URL.Path == "/control/telemetry" && r.Method == "GET" {
		s.mu.Lock()
		port := s.port
		s.mu.Unlock()
		if port == 0 {
			http.Error(w, "connect API first", 409)
			return
		}
		path := "/api/v1/gpus"
		if id := r.URL.Query().Get("uuid"); id != "" {
			path += "/" + url.PathEscape(id) + "/telemetry"
		}
		q := url.Values{}
		for _, k := range []string{"start_time", "end_time"} {
			if v := r.URL.Query().Get(k); v != "" {
				q.Set(k, v)
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s?%s", port, path, q.Encode()), nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		io.Copy(w, io.LimitReader(res.Body, 64<<20))
		return
	}
	if r.URL.Path != "/control/action" || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var in request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if in.Streamers < 0 || in.Streamers > 10 || in.Collectors < 0 || in.Collectors > 10 {
		http.Error(w, "replicas must be between 1 and 10, or blank for defaults", 400)
		return
	}
	s.mu.Lock()
	if in.Action == "cancel" {
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		w.WriteHeader(202)
		return
	}
	if s.phase == "running" {
		s.mu.Unlock()
		http.Error(w, "an operation is already running", 409)
		return
	}
	allowed := map[string]bool{"check": true, "tools": true, "cluster": true, "build": true, "deploy": true, "verify": true, "connect": true, "disconnect": true, "stop": true, "status": true, "cleanup": true, "remove-environment": true}
	if !allowed[in.Action] {
		s.mu.Unlock()
		http.Error(w, "unknown action", 400)
		return
	}
	if (in.Action == "cleanup" || in.Action == "remove-environment") && in.Confirmation != cleanupConfirmation(in.Action) {
		s.mu.Unlock()
		http.Error(w, "type the displayed cleanup confirmation", 400)
		return
	}
	if in.Root != "" && in.Root != s.root {
		if err := validRoot(in.Root); err != nil {
			s.mu.Unlock()
			http.Error(w, err.Error(), 400)
			return
		}
		if s.forward != nil {
			s.mu.Unlock()
			http.Error(w, "disconnect API before changing repository", 409)
			return
		}
		s.root = in.Root
		s.tag = savedTag(s.root)
		s.results = map[string]string{}
	}
	s.action = in.Action
	s.phase = "running"
	s.log = ""
	s.results[in.Action] = "running"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	s.cancel = cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		err := s.execute(ctx, in)
		if err != nil {
			fmt.Fprintf(s, "\nERROR: %v\n", err)
		} else {
			fmt.Fprintln(s, "\nCompleted successfully.")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.phase = "passed"
		if err != nil {
			s.phase = "failed"
		}
		s.results[in.Action] = s.phase
		s.cancel = nil
	}()
	w.WriteHeader(202)
}
func main() {
	root := flag.String("root", "", "repository directory")
	noOpen := flag.Bool("no-open", false, "do not open browser")
	flag.Parse()
	if *root == "" {
		exe, _ := os.Executable()
		*root = filepath.Dir(filepath.Dir(exe))
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		panic(err)
	}
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		panic(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &server{root: abs, host: l.Addr().String(), token: hex.EncodeToString(b), tag: savedTag(abs), phase: "idle", results: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/control/", s.api)
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	h := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go h.Serve(l)
	address := "http://" + s.host + "/#" + s.token
	fmt.Println("GPU Telemetry Console:", address)
	fmt.Println("Press Ctrl-C to quit the console. Cluster workloads will keep running.")
	if !*noOpen {
		_ = exec.Command("/usr/bin/open", address).Run()
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.stopForward()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.Shutdown(ctx)
}
