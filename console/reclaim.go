package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Only the two known local data directories may be adopted by the current
// single-node Minikube provisioner. Never remove finalizers or delete PVs by hand.
func recoverable(v object) bool {
	claim := v.Spec.ClaimRef
	if claim == nil || claim.Namespace != project || v.Status.Phase != "Released" || v.Spec.PersistentVolumeReclaimPolicy != "Delete" || v.Spec.HostPath == nil {
		return false
	}
	if claim.Name != project+"-postgresql-data" && claim.Name != project+"-queue-data" {
		return false
	}
	return v.Spec.HostPath.Path == "/tmp/hostpath-provisioner/"+project+"/"+claim.Name && v.Spec.StorageClassName == "standard" && v.Metadata.Annotations["pv.kubernetes.io/provisioned-by"] == "k8s.io/minikube-hostpath" && v.Metadata.Annotations["hostPathProvisionerIdentity"] != "" && v.Metadata.UID != "" && v.Metadata.ResourceVersion != ""
}
func (c cleaner) recoverReleasedVolumes(ctx context.Context) error {
	raw, err := c.kubectl(ctx, "get", "pv", "-o", "json")
	if err != nil {
		return err
	}
	all, err := decodeList(raw)
	if err != nil {
		return err
	}
	var targets []object
	for _, v := range all {
		if recoverable(v) {
			targets = append(targets, v)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	raw, err = c.kubectl(ctx, "get", "namespace", project, "--ignore-not-found", "-o", "name")
	if err != nil {
		return err
	}
	if raw != "" {
		return fmt.Errorf("refusing volume recovery while project namespace exists")
	}
	raw, err = c.kubectl(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	nodes, err := decodeList(raw)
	if err != nil {
		return err
	}
	if len(nodes) != 1 || nodes[0].Metadata.Name != project {
		return fmt.Errorf("identity recovery requires the dedicated single-node Minikube cluster")
	}
	raw, err = c.kubectl(ctx, "-n", "kube-system", "get", "pod", "storage-provisioner", "-o", "jsonpath={.spec.nodeName}")
	if err != nil {
		return err
	}
	if raw != project {
		return fmt.Errorf("storage provisioner is not on the expected node")
	}
	for _, v := range targets {
		for _, other := range all {
			// A failed Minikube reclamation can leave an old project PV while a
			// later claim reuses the provisioner's deterministic project path.
			// Permit the duplicate only when both PVs independently satisfy every
			// project ownership and lifecycle check above.
			if other.Metadata.Name != v.Metadata.Name && other.Spec.HostPath != nil && other.Spec.HostPath.Path == v.Spec.HostPath.Path && !recoverable(other) {
				return fmt.Errorf("volume path is shared by %s and %s", v.Metadata.Name, other.Metadata.Name)
			}
		}
	}
	// A fresh claim gives the actual hostPath identity. The leader-election ID in
	// provisioner logs is different and must never be used for this annotation.
	fmt.Fprintln(c.log, "Checking Minikube provisioner identity for released project volumes.")
	return c.withProvisionerIdentity(ctx, func(identity string) error {
		for _, v := range targets {
			old := v.Metadata.Annotations["hostPathProvisionerIdentity"]
			if old == identity {
				continue
			}
			patch := []map[string]any{
				{"op": "test", "path": "/metadata/uid", "value": v.Metadata.UID},
				{"op": "test", "path": "/metadata/resourceVersion", "value": v.Metadata.ResourceVersion},
				{"op": "test", "path": "/status/phase", "value": "Released"},
				{"op": "test", "path": "/metadata/annotations/hostPathProvisionerIdentity", "value": old},
				{"op": "replace", "path": "/metadata/annotations/hostPathProvisionerIdentity", "value": identity},
			}
			data, _ := json.Marshal(patch)
			if _, err := c.kubectl(ctx, "patch", "pv", v.Metadata.Name, "--type=json", "-p", string(data)); err != nil {
				return fmt.Errorf("could not safely adopt %s: %w", v.Metadata.Name, err)
			}
			fmt.Fprintf(c.log, "Reassigned %s to the current local provisioner; waiting for normal data reclamation.\n", v.Metadata.Name)
		}
		return nil
	})
}

func (c cleaner) withProvisionerIdentity(ctx context.Context, use func(string) error) (result error) {
	raw, err := c.kubectl(ctx, "get", "storageclass", "standard", "-o", "json")
	if err != nil {
		return err
	}
	var sc struct {
		Provisioner       string `json:"provisioner"`
		ReclaimPolicy     string `json:"reclaimPolicy"`
		VolumeBindingMode string `json:"volumeBindingMode"`
	}
	if err = json.Unmarshal([]byte(raw), &sc); err != nil {
		return err
	}
	if sc.Provisioner != "k8s.io/minikube-hostpath" || sc.ReclaimPolicy != "Delete" || sc.VolumeBindingMode != "Immediate" {
		return fmt.Errorf("unexpected standard StorageClass; cannot safely probe identity")
	}
	nonce := make([]byte, 6)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	ns := "gpu-telemetry-reclaim-" + hex.EncodeToString(nonce)
	if _, err = c.kubectl(ctx, "create", "namespace", ns); err != nil {
		return err
	}
	defer func() {
		// Always remove only the temporary namespace this invocation created, even
		// when the user cancels recovery. Do not hide a probe-cleanup failure.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		raw, e := c.kubectl(cleanupCtx, "get", "pv", "-o", "json")
		var probeVolumes []object
		if e == nil {
			probeVolumes, e = decodeList(raw)
		}
		_, deleteErr := c.kubectl(cleanupCtx, "delete", "namespace", ns, "--wait=true", "--timeout=120s")
		if e == nil {
			e = deleteErr
		}
		if e == nil {
			for _, v := range probeVolumes {
				if v.Spec.ClaimRef != nil && v.Spec.ClaimRef.Namespace == ns {
					e = c.waitVolumeGone(cleanupCtx, v.Metadata.Name, "60s")
					if e != nil {
						break
					}
				}
			}
		}
		if e != nil {
			result = fmt.Errorf("recovery result: %v; temporary probe cleanup failed (%s): %w", result, ns, e)
		}
	}()
	manifest := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "identity-probe", "namespace": ns}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "standard", "resources": map[string]any{"requests": map[string]string{"storage": "1Mi"}}}}
	file, err := os.CreateTemp("", "gpu-telemetry-reclaim-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = json.NewEncoder(file).Encode(manifest); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, err = c.kubectl(ctx, "create", "-f", file.Name()); err != nil {
		return err
	}
	if _, err = c.kubectl(ctx, "-n", ns, "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/identity-probe", "--timeout=60s"); err != nil {
		return err
	}
	raw, err = c.kubectl(ctx, "-n", ns, "get", "pvc", "identity-probe", "-o", "json")
	if err != nil {
		return err
	}
	var pvc object
	if err = json.Unmarshal([]byte(raw), &pvc); err != nil {
		return err
	}
	if pvc.Spec.VolumeName == "" {
		return fmt.Errorf("probe claim has no volume")
	}
	raw, err = c.kubectl(ctx, "get", "pv", pvc.Spec.VolumeName, "-o", "json")
	if err != nil {
		return err
	}
	var pv object
	if err = json.Unmarshal([]byte(raw), &pv); err != nil {
		return err
	}
	identity := pv.Metadata.Annotations["hostPathProvisionerIdentity"]
	if identity == "" || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != ns || pv.Spec.ClaimRef.Name != "identity-probe" || pv.Metadata.Annotations["pv.kubernetes.io/provisioned-by"] != "k8s.io/minikube-hostpath" {
		return fmt.Errorf("invalid provisioner identity probe")
	}
	return use(identity)
}
