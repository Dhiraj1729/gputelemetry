package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func releasedVolume() object {
	var v object
	json.Unmarshal([]byte(`{"metadata":{"name":"old-pv","uid":"stable-uid","resourceVersion":"10","annotations":{"hostPathProvisionerIdentity":"old-identity","pv.kubernetes.io/provisioned-by":"k8s.io/minikube-hostpath"}},"spec":{"storageClassName":"standard","persistentVolumeReclaimPolicy":"Delete","claimRef":{"namespace":"gpu-telemetry","name":"gpu-telemetry-queue-data"},"hostPath":{"path":"/tmp/hostpath-provisioner/gpu-telemetry/gpu-telemetry-queue-data"}},"status":{"phase":"Released"}}`), &v)
	return v
}
func TestRecoveryEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*object)
	}{
		{"bound", func(v *object) { v.Status.Phase = "Bound" }},
		{"foreign namespace", func(v *object) { v.Spec.ClaimRef.Namespace = "other" }},
		{"foreign claim", func(v *object) { v.Spec.ClaimRef.Name = "other" }},
		{"foreign directory", func(v *object) { v.Spec.HostPath.Path = "/data/other" }},
		{"retain", func(v *object) { v.Spec.PersistentVolumeReclaimPolicy = "Retain" }},
		{"other provisioner", func(v *object) { v.Metadata.Annotations["pv.kubernetes.io/provisioned-by"] = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := releasedVolume()
			tc.change(&v)
			if recoverable(v) {
				t.Fatal("unsafe adoption accepted")
			}
		})
	}
	if !recoverable(releasedVolume()) {
		t.Fatal("valid project volume rejected")
	}
}

func TestDuplicateVerifiedProjectPathsAreRecoverable(t *testing.T) {
	v1 := releasedVolume()
	v2 := releasedVolume()
	v2.Metadata.Name = "new-pv"
	v2.Metadata.UID = "new-uid"
	v2.Metadata.ResourceVersion = "11"
	v2.Metadata.Annotations["hostPathProvisionerIdentity"] = "new-identity"
	if !recoverable(v1) || !recoverable(v2) {
		t.Fatal("verified duplicate project volumes should remain eligible")
	}
}
func TestIdentityRecovery(t *testing.T) {
	for _, failPatch := range []bool{false, true} {
		ns := ""
		patched := false
		cleaned := false
		v := releasedVolume()
		call := func(ctx context.Context, name string, args ...string) (string, error) {
			cmd := strings.Join(args, " ")
			switch {
			case strings.Contains(cmd, "get namespace gpu-telemetry"):
				return "", nil
			case strings.Contains(cmd, "get nodes"):
				return `{"items":[{"metadata":{"name":"gpu-telemetry"}}]}`, nil
			case strings.Contains(cmd, "get pod storage-provisioner"):
				return project, nil
			case strings.Contains(cmd, "get storageclass"):
				return `{"provisioner":"k8s.io/minikube-hostpath","reclaimPolicy":"Delete","volumeBindingMode":"Immediate"}`, nil
			case strings.Contains(cmd, "create namespace"):
				ns = args[len(args)-1]
				return "", nil
			case strings.Contains(cmd, "delete namespace"):
				if !strings.Contains(cmd, ns) || ns == "" {
					t.Fatal("wrong probe namespace")
				}
				cleaned = true
				return "", nil
			case strings.Contains(cmd, "get pvc identity-probe"):
				return `{"spec":{"volumeName":"probe-pv"}}`, nil
			case strings.Contains(cmd, "get pv probe-pv"):
				return fmt.Sprintf(`{"metadata":{"annotations":{"hostPathProvisionerIdentity":"new-identity","pv.kubernetes.io/provisioned-by":"k8s.io/minikube-hostpath"}},"spec":{"claimRef":{"namespace":%q,"name":"identity-probe"}}}`, ns), nil
			case strings.Contains(cmd, "get pv -o json"):
				b, _ := json.Marshal(map[string]any{"items": []object{v}})
				return string(b), nil
			case strings.Contains(cmd, "patch pv old-pv"):
				if !strings.Contains(cmd, `"path":"/metadata/uid","value":"stable-uid"`) || !strings.Contains(cmd, `"path":"/metadata/resourceVersion","value":"10"`) || !strings.Contains(cmd, `"value":"new-identity"`) {
					t.Fatalf("missing compare-and-swap checks: %s", cmd)
				}
				if failPatch {
					return "", fmt.Errorf("resource version changed")
				}
				patched = true
				return "", nil
			default:
				return "", nil
			}
		}
		err := (cleaner{call: call, log: io.Discard}).recoverReleasedVolumes(context.Background())
		if (err != nil) != failPatch {
			t.Fatalf("unexpected recovery error %v", err)
		}
		if !cleaned {
			t.Fatal("probe namespace leaked")
		}
		if !failPatch && !patched {
			t.Fatal("identity not repaired")
		}
	}
}
