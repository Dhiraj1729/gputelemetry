package main

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type cleanupFixture struct {
	calls                             []string
	foreign, retain, stuck, foreignVM bool
	deleted                           bool
}

func (f *cleanupFixture) call(ctx context.Context, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	switch {
	case strings.Contains(cmd, "api-resources"):
		return "pods\nsecrets\n", nil
	case strings.Contains(cmd, "-n gpu-telemetry get pods"):
		label := "gpu-telemetry"
		if f.foreign {
			label = "another-project"
		}
		return fmt.Sprintf(`{"items":[{"kind":"Pod","metadata":{"name":"worker","labels":{"app.kubernetes.io/instance":%q}}}]}`, label), nil
	case strings.Contains(cmd, " get pv -o json"):
		if f.deleted && !f.stuck {
			return `{"items":[]}`, nil
		}
		policy := "Delete"
		if f.retain {
			policy = "Retain"
		}
		return fmt.Sprintf(`{"items":[{"metadata":{"name":"pvc-example"},"spec":{"persistentVolumeReclaimPolicy":%q,"claimRef":{"namespace":"gpu-telemetry","name":"gpu-telemetry-queue-data"}}}]}`, policy), nil
	case strings.Contains(cmd, "get namespaces"):
		return `{"items":[{"metadata":{"name":"gpu-telemetry"}},{"metadata":{"name":"default"}}]}`, nil
	case strings.Contains(cmd, "get namespace gpu-telemetry"):
		return "", nil
	case strings.Contains(cmd, "delete namespace"):
		f.deleted = true
		return "", nil
	case strings.Contains(cmd, "wait --for=delete"):
		if f.stuck {
			return "", fmt.Errorf("timeout")
		}
		return "", nil
	case strings.Contains(cmd, "get pv pvc-example"):
		return "pvc-example", nil
	case strings.Contains(cmd, "docker --context"):
		if f.foreignVM {
			return "other-app", nil
		}
		return "gpu-telemetry", nil
	case strings.Contains(cmd, "profile list"):
		return `{"valid":[],"invalid":[]}`, nil
	case strings.Contains(cmd, "colima list"):
		return "", nil
	case strings.Contains(cmd, " get "):
		return `{"items":[]}`, nil
	default:
		return "", nil
	}
}
func TestCleanupOrderingAndScope(t *testing.T) {
	for _, remove := range []bool{false, true} {
		f := &cleanupFixture{}
		c := cleaner{call: f.call, log: io.Discard}
		if err := c.clean(context.Background(), remove); err != nil {
			t.Fatal(err)
		}
		all := strings.Join(f.calls, "\n")
		uninstall := strings.Index(all, "helm uninstall gpu-telemetry")
		deleteNS := strings.Index(all, "delete namespace gpu-telemetry")
		wait := strings.Index(all, "wait --for=delete pv/pvc-example")
		if uninstall < 0 || deleteNS < uninstall || wait < deleteNS {
			t.Fatal(all)
		}
		if remove {
			idx := strings.Index(all, "minikube delete -p gpu-telemetry")
			colima := strings.Index(all, "colima delete gpu-telemetry --force --data")
			if idx < wait || colima < idx {
				t.Fatal(all)
			}
		} else if strings.Contains(all, "minikube delete") {
			t.Fatal("ordinary cleanup deleted environment")
		}
	}
}
func TestCleanupRefusesForeignAndRetainedResources(t *testing.T) {
	for _, f := range []*cleanupFixture{{foreign: true}, {retain: true}, {foreignVM: true}} {
		c := cleaner{call: f.call, log: io.Discard}
		if err := c.clean(context.Background(), true); err == nil {
			t.Fatal("unsafe cleanup accepted")
		}
		for _, cmd := range f.calls {
			if strings.Contains(cmd, " uninstall ") || strings.Contains(cmd, " delete ") {
				t.Fatalf("mutated after unsafe inventory: %s", cmd)
			}
		}
	}
}
func TestCleanupDoesNotRemoveEnvironmentAfterVolumeFailure(t *testing.T) {
	f := &cleanupFixture{stuck: true}
	c := cleaner{call: f.call, log: io.Discard}
	if err := c.clean(context.Background(), true); err == nil {
		t.Fatal("expected reclamation failure")
	}
	for _, cmd := range f.calls {
		if strings.Contains(cmd, "minikube delete") || strings.Contains(cmd, "colima delete") {
			t.Fatal("environment removed on failed verification")
		}
	}
}
func TestCleanupRequiresConfirmation(t *testing.T) {
	for _, action := range []string{"cleanup", "remove-environment"} {
		s := &server{token: "secret", host: "127.0.0.1:7777"}
		r := httptest.NewRequest("POST", "http://127.0.0.1:7777/control/action", strings.NewReader(fmt.Sprintf(`{"action":%q}`, action)))
		r.Header.Set("X-Console-Token", "secret")
		w := httptest.NewRecorder()
		s.api(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
func TestCleanupCancellationPreventsDeletion(t *testing.T) {
	f := &cleanupFixture{}
	c := cleaner{call: f.call, log: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.clean(ctx, true); err == nil {
		t.Fatal("cancelled cleanup succeeded")
	}
	if len(f.calls) != 0 {
		t.Fatal("commands executed after cancellation")
	}
}

func TestAlreadyDeletedVolumeIsSuccess(t *testing.T) {
	c := cleaner{call: func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "wait --for=delete") {
			return "", fmt.Errorf("NotFound")
		}
		return "", nil
	}}
	if err := c.waitVolumeGone(context.Background(), "gone-pv", "60s"); err != nil {
		t.Fatal(err)
	}
}
