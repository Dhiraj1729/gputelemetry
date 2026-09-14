//go:build packaging

package packaging

import (
	"bytes"
	"encoding/base64"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type doc = map[string]any

func object(v any) doc { return v.(map[string]any) }
func nested(v doc, keys ...string) any {
	var cur any = v
	for _, k := range keys {
		cur = object(cur)[k]
	}
	return cur
}
func render(t *testing.T, values ...string) []doc {
	t.Helper()
	args := []string{"template", "gpu-test", "../../deploy/helm/gpu-telemetry", "-n", "gpu-test"}
	for _, value := range values {
		args = append(args, "--set", value)
	}
	cmd := exec.Command("helm", args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm failed: %v %s", err, b)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var out []doc
	for {
		var d doc
		if err := dec.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	return out
}
func find(t *testing.T, docs []doc, kind, name string) doc {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && nested(d, "metadata", "name") == name {
			return d
		}
	}
	t.Fatalf("missing %s/%s", kind, name)
	return nil
}
func TestByteLimitArgumentsUseDecimalIntegers(t *testing.T) {
	// Keep the byte limits from values.yaml: --set parses numbers differently
	// and can hide scientific notation introduced by loading numeric YAML.
	docs := render(t, "bootstrapOnly=false", "database.existingSecret=existing-database")
	for _, tc := range []struct{ kind, name, prefix string }{
		{"StatefulSet", "gpu-test-queue", "--max-logical-bytes="},
		{"Deployment", "gpu-test-api", "--max-response-bytes="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := find(t, docs, tc.kind, tc.name)
			container := object(nested(d, "spec", "template", "spec", "containers").([]any)[0])
			found := false
			for _, arg := range toStrings(container["args"].([]any)) {
				if strings.HasPrefix(arg, tc.prefix) {
					found = true
					if want := tc.prefix + "67108864"; arg != want {
						t.Errorf("argument = %q, want exact decimal integer %q (no scientific notation)", arg, want)
					}
				}
			}
			if !found {
				t.Fatalf("missing argument %s", tc.prefix)
			}
		})
	}
}

func TestFullChartInvariants(t *testing.T) {
	docs := render(t, "bootstrapOnly=false", "database.existingSecret=existing-database")
	for _, component := range []string{"queue", "postgresql", "streamer", "collector", "api"} {
		kind := "Deployment"
		if component == "queue" || component == "postgresql" {
			kind = "StatefulSet"
		}
		d := find(t, docs, kind, "gpu-test-"+component)
		if nested(d, "spec", "replicas") != 1 {
			t.Fatal(component, "replica default")
		}
		selector := nested(d, "spec", "selector", "matchLabels")
		labels := nested(d, "spec", "template", "metadata", "labels")
		if !reflect.DeepEqual(selector, labels) {
			t.Fatal(component, "selector mismatch")
		}
		pod := object(nested(d, "spec", "template", "spec"))
		if pod["terminationGracePeriodSeconds"] != 30 || pod["automountServiceAccountToken"] != false {
			t.Fatal(component, "unsafe pod defaults")
		}
		if nested(pod, "securityContext", "runAsNonRoot") != true {
			t.Fatal(component, "root pod")
		}
		container := object(pod["containers"].([]any)[0])
		if nested(container, "securityContext", "readOnlyRootFilesystem") != true || nested(container, "securityContext", "allowPrivilegeEscalation") != false {
			t.Fatal(component, "unsafe container")
		}
		if container["resources"] == nil {
			t.Fatal(component, "missing resources")
		}
		if component == "streamer" {
			if container["livenessProbe"] != nil || container["readinessProbe"] != nil || container["startupProbe"] != nil {
				t.Fatal("streamer must have no invented HTTP probes")
			}
			args := container["args"].([]any)
			if !strings.Contains(strings.Join(toStrings(args), " "), "--csv=/app/data/metrics.csv") {
				t.Fatal("CSV path")
			}
		}
		if component == "api" || component == "collector" {
			env := container["env"].([]any)
			if nested(object(env[0]), "valueFrom", "secretKeyRef", "name") != "existing-database" {
				t.Fatal(component, "missing secret")
			}
			if nested(container, "readinessProbe", "httpGet", "path") != "/readyz" {
				t.Fatal(component, "readiness")
			}
		}
		if component != "streamer" {
			service := find(t, docs, "Service", "gpu-test-"+component)
			if nested(service, "spec", "type") != "ClusterIP" || !reflect.DeepEqual(nested(service, "spec", "selector"), selector) {
				t.Fatal(component, "service selector/type")
			}
		}
	}
	for _, component := range []string{"queue", "postgresql"} {
		pvc := find(t, docs, "PersistentVolumeClaim", "gpu-test-"+component+"-data")
		if nested(pvc, "metadata", "annotations", "helm.sh/resource-policy") != "keep" {
			t.Fatal("PVC retention")
		}
		if object(pvc["spec"])["storageClassName"] != nil {
			t.Fatal("must use default StorageClass when unset")
		}
	}
	job := find(t, docs, "Job", "gpu-test-migrate")
	if nested(job, "metadata", "annotations", "helm.sh/hook") != "pre-install,pre-upgrade" {
		t.Fatal("wrong migration hook")
	}
	if nested(job, "metadata", "annotations", "helm.sh/hook-delete-policy") != "before-hook-creation" {
		t.Fatal("migration evidence retention")
	}
	jobContainer := object(nested(job, "spec", "template", "spec", "containers").([]any)[0])
	if jobContainer["image"] != "gpu-telemetry/migrate:dev" {
		t.Fatal("dedicated migration image")
	}
	for _, d := range docs {
		if d["kind"] == "Ingress" || d["kind"] == "ConfigMap" || d["kind"] == "Secret" {
			t.Fatal("unexpected resource", d["kind"])
		}
	}
}
func toStrings(a []any) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = v.(string)
	}
	return out
}
func TestBootstrapAndDevelopmentSecret(t *testing.T) {
	docs := render(t)
	find(t, docs, "StatefulSet", "gpu-test-postgresql")
	secret := find(t, docs, "Secret", "gpu-test-database")
	if nested(secret, "metadata", "annotations", "helm.sh/resource-policy") != "keep" {
		t.Fatal("secret must survive uninstall")
	}
	data := object(secret["data"])
	pw, err := base64.StdEncoding.DecodeString(data["postgres-password"].(string))
	if err != nil || len(pw) != 48 {
		t.Fatal("bad generated password")
	}
	url, err := base64.StdEncoding.DecodeString(data["database-url"].(string))
	if err != nil || !strings.Contains(string(url), "@gpu-test-postgresql:5432/telemetry") || !strings.Contains(string(url), string(pw)) {
		t.Fatal("bad generated database URL")
	}
	for _, d := range docs {
		if d["kind"] == "Job" || d["kind"] == "Deployment" {
			t.Fatal("apps/migration must wait for bootstrap DB")
		}
		if d["kind"] == "PersistentVolumeClaim" && nested(d, "metadata", "name") == "gpu-test-queue-data" {
			t.Fatal("bootstrap must not wait for unused queue PVC")
		}
	}
}
func TestValuesOverridesAndGuards(t *testing.T) {
	docs := render(t, "bootstrapOnly=false", "database.existingSecret=existing-database", "images.repository=ghcr.io/example/gpu", "images.tag=v1", "streamer.replicas=2", "collector.replicas=2", "queue.storage.storageClass=custom")
	d := find(t, docs, "Deployment", "gpu-test-streamer")
	if nested(d, "spec", "replicas") != 2 {
		t.Fatal("scaling override")
	}
	c := object(nested(d, "spec", "template", "spec", "containers").([]any)[0])
	if c["image"] != "ghcr.io/example/gpu/streamer:v1" {
		t.Fatal("image override")
	}
	if nested(find(t, docs, "PersistentVolumeClaim", "gpu-test-queue-data"), "spec", "storageClassName") != "custom" {
		t.Fatal("storage override")
	}
	for _, setting := range []string{"queue.replicas=2", "queue.dedupCapacity=1", "database.password=forbidden-in-values", "collector.workers=11", "database.developmentSecret=false", "streamer.rate=0", "api.poolMax=11"} {
		cmd := exec.Command("helm", "template", "bad", "../../deploy/helm/gpu-telemetry", "--set", setting)
		if err := cmd.Run(); err == nil {
			t.Fatal("unsafe setting accepted", setting)
		}
	}
}

// Stub only CLI transport; this verifies orchestration, not Kubernetes readiness.
func TestInstallerPhases(t *testing.T) {
	for _, tc := range []struct{ existing, namespace string }{
		{"false", ""}, {"true", ""}, {"true", "gpu-telemetry-day5"},
	} {
		existing := tc.existing
		t.Run(existing+"/"+tc.namespace, func(t *testing.T) {
			temp := t.TempDir()
			calls := filepath.Join(temp, "calls")
			helm := `#!/usr/bin/env bash
set -eu
printf 'helm %s\n' "$*" >> "$FAKE_CALLS"
for arg in "$@"; do if [[ "$arg" == --all ]]; then exit 2; fi; done
if [[ "$1" == list ]]; then
 if [[ "$FAKE_EXISTING" == true ]]; then printf '[{"name":"gpu-telemetry"}]\n'; else printf '[]\n'; fi
fi
`
			kubectl := `#!/usr/bin/env bash
set -eu
printf 'kubectl %s\n' "$*" >> "$FAKE_CALLS"
`
			for name, body := range map[string]string{"helm": helm, "kubectl": kubectl} {
				if err := os.WriteFile(filepath.Join(temp, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "../../scripts/helm-install.sh")
			cmd.Env = append(os.Environ(), "PATH="+temp+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_CALLS="+calls, "FAKE_EXISTING="+existing, "RELEASE=gpu-telemetry", "VALUES_FILE=", "NAMESPACE="+tc.namespace)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("simulated installer: %v %s", err, b)
			}
			b, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			text := string(b)
			namespace := tc.namespace
			if namespace == "" {
				namespace = "gpu-telemetry"
			}
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				if strings.Contains(line, "cluster-info") {
					continue
				}
				if !strings.Contains(line, "-n "+namespace+" ") {
					t.Fatalf("command did not target selected namespace %q: %s", namespace, line)
				}
			}
			install := strings.Index(text, "helm install")
			upgrade := strings.Index(text, "helm upgrade")
			ready := strings.Index(text, "rollout status statefulset/gpu-telemetry-postgresql")
			if upgrade < 0 || ready < 0 || ready > upgrade || !strings.Contains(text, "--set bootstrapOnly=false") {
				t.Fatal("missing DB-before-migration sequencing", text)
			}
			if existing == "false" && (install < 0 || install > ready || !strings.Contains(text, "--set bootstrapOnly=true")) {
				t.Fatal("fresh install not bootstrapped", text)
			}
			if existing == "true" && install >= 0 {
				t.Fatal("existing release bootstrapped again", text)
			}
		})
	}
}
