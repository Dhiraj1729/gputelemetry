package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const project = "gpu-telemetry"

func cleanupConfirmation(action string) string {
	if action == "remove-environment" {
		return "REMOVE gpu-telemetry"
	}
	return "DELETE gpu-telemetry"
}

type cleaner struct {
	call func(context.Context, string, ...string) (string, error)
	log  io.Writer
}
type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		UID             string            `json:"uid"`
		ResourceVersion string            `json:"resourceVersion"`
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		Labels          map[string]string `json:"labels"`
		Annotations     map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		HostPath *struct {
			Path string `json:"path"`
		} `json:"hostPath"`
		StorageClassName              string `json:"storageClassName"`
		VolumeName                    string `json:"volumeName"`
		PersistentVolumeReclaimPolicy string `json:"persistentVolumeReclaimPolicy"`
		ClaimRef                      *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

func (c cleaner) kubectl(ctx context.Context, args ...string) (string, error) {
	return c.call(ctx, "/opt/homebrew/bin/kubectl", append([]string{"--context", project, "--request-timeout=15s"}, args...)...)
}
func (c cleaner) waitVolumeGone(ctx context.Context, name, timeout string) error {
	_, err := c.kubectl(ctx, "wait", "--for=delete", "pv/"+name, "--timeout="+timeout)
	if err == nil {
		return nil
	}
	raw, checkErr := c.kubectl(ctx, "get", "pv", name, "--ignore-not-found", "-o", "name")
	if checkErr == nil && strings.TrimSpace(raw) == "" {
		return nil
	}
	return fmt.Errorf("volume %s was not reclaimed: %w", name, err)
}
func decodeList(raw string) ([]object, error) {
	var list struct {
		Items []object `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	if list.Items == nil {
		return nil, fmt.Errorf("invalid Kubernetes list response")
	}
	return list.Items, nil
}
func owned(o object) bool {
	if o.Kind == "Event" {
		return true
	}
	if o.Kind == "ServiceAccount" && o.Metadata.Name == "default" {
		return true
	}
	if o.Kind == "ConfigMap" && o.Metadata.Name == "kube-root-ca.crt" {
		return true
	}
	l := o.Metadata.Labels
	a := o.Metadata.Annotations
	return l["app.kubernetes.io/instance"] == project || (o.Kind == "Secret" && l["owner"] == "helm" && l["name"] == project) || (a["meta.helm.sh/release-name"] == project && a["meta.helm.sh/release-namespace"] == project)
}
func (c cleaner) clean(ctx context.Context, remove bool) error {
	// Inventory every listable namespaced resource, including custom resources.
	raw, err := c.kubectl(ctx, "api-resources", "--verbs=list", "--namespaced=true", "-o", "name")
	if err != nil {
		return err
	}
	kinds := strings.Fields(raw)
	if len(kinds) == 0 {
		return fmt.Errorf("resource discovery returned nothing")
	}
	for _, kind := range kinds {
		raw, err = c.kubectl(ctx, "-n", project, "get", kind, "-o", "json")
		if err != nil {
			return err
		}
		items, e := decodeList(raw)
		if e != nil {
			return e
		}
		for _, o := range items {
			if !owned(o) {
				return fmt.Errorf("refusing namespace deletion: unrecognized %s/%s", o.Kind, o.Metadata.Name)
			}
		}
	}
	raw, err = c.kubectl(ctx, "get", "pv", "-o", "json")
	if err != nil {
		return err
	}
	pvs, err := decodeList(raw)
	if err != nil {
		return err
	}
	var volumes []string
	for _, v := range pvs {
		if v.Spec.ClaimRef != nil && v.Spec.ClaimRef.Namespace == project {
			if v.Spec.PersistentVolumeReclaimPolicy != "Delete" {
				return fmt.Errorf("volume %s uses %s reclaim policy; automatic data cleanup cannot be verified", v.Metadata.Name, v.Spec.PersistentVolumeReclaimPolicy)
			}
			volumes = append(volumes, v.Metadata.Name)
		} else if remove {
			return fmt.Errorf("refusing environment removal: unrelated persistent volume %s", v.Metadata.Name)
		}
	}
	if remove {
		raw, err = c.kubectl(ctx, "get", "namespaces", "-o", "json")
		if err != nil {
			return err
		}
		namespaces, e := decodeList(raw)
		if e != nil {
			return e
		}
		for _, n := range namespaces {
			switch n.Metadata.Name {
			case project, "kube-system", "kube-public", "kube-node-lease", "default":
			default:
				return fmt.Errorf("refusing environment removal: unrelated namespace %s", n.Metadata.Name)
			}
		}
		// Default namespace may exist, but may not contain another application's resources.
		for _, kind := range kinds {
			raw, err = c.kubectl(ctx, "-n", "default", "get", kind, "-o", "json")
			if err != nil {
				return err
			}
			items, e := decodeList(raw)
			if e != nil {
				return e
			}
			for _, o := range items {
				if o.Kind == "Service" && o.Metadata.Name == "kubernetes" {
					continue
				}
				if o.Kind == "Event" || (o.Kind == "ServiceAccount" && o.Metadata.Name == "default") || (o.Kind == "ConfigMap" && o.Metadata.Name == "kube-root-ca.crt") {
					continue
				}
				return fmt.Errorf("refusing environment removal: default namespace contains %s/%s", o.Kind, o.Metadata.Name)
			}
		}
		raw, err = c.call(ctx, "/opt/homebrew/bin/docker", "--context", "colima-gpu-telemetry", "ps", "-a", "--format", "{{.Names}}")
		if err != nil {
			return err
		}
		for _, name := range strings.Fields(raw) {
			if name != project {
				return fmt.Errorf("refusing VM removal: unrelated container %s", name)
			}
		}
		raw, err = c.call(ctx, "/opt/homebrew/bin/docker", "--context", "colima-gpu-telemetry", "volume", "ls", "--format", "{{.Name}}")
		if err != nil {
			return err
		}
		for _, name := range strings.Fields(raw) {
			if name != project {
				return fmt.Errorf("refusing VM removal: unrecognized Docker volume %s", name)
			}
		}
	}
	fmt.Fprintln(c.log, "Ownership checks passed. Removing Helm deployment and retained data.")
	if _, err = c.call(ctx, "/opt/homebrew/bin/helm", "uninstall", project, "--kube-context", project, "-n", project, "--ignore-not-found", "--wait", "--timeout", "3m"); err != nil {
		return err
	}
	if _, err = c.kubectl(ctx, "delete", "namespace", project, "--ignore-not-found", "--wait=true", "--timeout=180s"); err != nil {
		return err
	}
	raw, err = c.kubectl(ctx, "get", "namespace", project, "--ignore-not-found", "-o", "name")
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) != "" {
		return fmt.Errorf("namespace still exists")
	}
	if err := c.recoverReleasedVolumes(ctx); err != nil {
		return err
	}
	for _, name := range volumes {
		if err = c.waitVolumeGone(ctx, name, "120s"); err != nil {
			// The volume may only have entered Released after the first recovery
			// inventory. Recheck now before declaring that reclamation is stuck.
			if recoveryErr := c.recoverReleasedVolumes(ctx); recoveryErr != nil {
				return recoveryErr
			}
			if err = c.waitVolumeGone(ctx, name, "120s"); err != nil {
				return err
			}
		}
	}
	raw, err = c.kubectl(ctx, "get", "pv", "-o", "json")
	if err != nil {
		return err
	}
	remaining, e := decodeList(raw)
	if e != nil {
		return e
	}
	for _, v := range remaining {
		if v.Spec.ClaimRef != nil && v.Spec.ClaimRef.Namespace == project {
			return fmt.Errorf("project volume remains: %s", v.Metadata.Name)
		}
	}
	fmt.Fprintln(c.log, "Namespace, PVCs and associated PVs removed. Local Minikube storage reclamation confirmed by Kubernetes; this is not secure disk erasure.")
	if !remove {
		return nil
	}
	if _, err = c.call(ctx, "/opt/homebrew/bin/minikube", "delete", "-p", project); err != nil {
		return err
	}
	raw, listErr := c.call(ctx, "/opt/homebrew/bin/minikube", "profile", "list", "-o", "json")
	var profiles struct {
		Valid   *[]struct{ Name string } `json:"valid"`
		Invalid *[]struct{ Name string } `json:"invalid"`
	}
	if e := json.Unmarshal([]byte(raw), &profiles); e != nil || profiles.Valid == nil || profiles.Invalid == nil {
		return fmt.Errorf("cannot verify Minikube profile removal: %v", listErr)
	}
	for _, group := range [][]struct{ Name string }{*profiles.Valid, *profiles.Invalid} {
		for _, p := range group {
			if p.Name == project {
				return fmt.Errorf("Minikube profile still exists")
			}
		}
	}
	if _, err = c.call(ctx, "/opt/homebrew/bin/colima", "delete", project, "--force", "--data"); err != nil {
		return err
	}
	// Verify named profile removal without relying on a stopped status exit code.
	raw, err = c.call(ctx, "/opt/homebrew/bin/colima", "list", "--json")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	for {
		var p struct {
			Name string `json:"name"`
		}
		e := dec.Decode(&p)
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if p.Name == project {
			return fmt.Errorf("Colima profile still exists")
		}
	}
	fmt.Fprintln(c.log, "Dedicated Minikube and Colima environments removed. Installed tools and host build caches were preserved.")
	return nil
}
