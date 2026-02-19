package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	AddonArtifactsCM = "kata-addon-artifacts"
	AddonMCName      = "99-kata-addon-kernel"
	AddonDestPath    = "/var/cache/kata-containers/vmlinuz.ibm-se"
	AddonScriptPath  = "/usr/local/bin/update-kata-kernel.sh"
)

func (r *KataConfigOpenShiftReconciler) EnsureAddonKernelMC(machinePool string) error {
	ctx := context.TODO()

	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      AddonArtifactsCM,
		Namespace: OperatorNamespace,
	}, cm); err != nil {
		if k8serrors.IsNotFound(err) {
			r.Log.Info("addon CM not found, skipping addon MC")
			return nil
		}
		return err
	}

	addonImage := strings.TrimSpace(cm.Data["addonImage"])
	kernelPath := strings.TrimSpace(cm.Data["kernelPath"])
	if addonImage == "" || kernelPath == "" {
		r.Log.Info("addonImage or kernelPath missing, skipping addon MC")
		return nil
	}

	r.Log.Info("Reconciling addon MachineConfig", "image", addonImage, "kernelPath", kernelPath)

	ignJSON, err := generateIgnitionJSON(addonImage, kernelPath)
	if err != nil {
		return fmt.Errorf("failed to generate ignition JSON: %w", err)
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: AddonMCName,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": machinePool,
				"app":                                    "sandboxed-containers-addon",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{Raw: ignJSON},
		},
	}

	if err := r.Client.Create(ctx, mc); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			existing := &mcfgv1.MachineConfig{}
			if getErr := r.Client.Get(ctx, types.NamespacedName{Name: AddonMCName}, existing); getErr != nil {
				return getErr
			}
			existing.Spec = mc.Spec
			if updErr := r.Client.Update(ctx, existing); updErr != nil {
				return updErr
			}
			r.Log.Info("addon MC updated")
			return nil
		}
		return err
	}

	r.Log.Info("addon MC created")
	return nil
}

func (r *KataConfigOpenShiftReconciler) DeleteAddonKernelMC() error {
	ctx := context.TODO()
	mc := &mcfgv1.MachineConfig{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: AddonMCName}, mc); err != nil {
		return err
	}
	r.Log.Info("Deleting addon MC")
	return r.Client.Delete(ctx, mc)
}

func generateIgnitionJSON(addonImage, kernelPath string) ([]byte, error) {
	script := renderKernelScript()
	script = strings.ReplaceAll(script, "ADDON_IMAGE", addonImage)
	script = strings.ReplaceAll(script, "KERNEL_PATH", kernelPath)

	ign := map[string]interface{}{
		"ignition": map[string]interface{}{"version": "3.2.0"},
		"storage": map[string]interface{}{
			"files": []map[string]interface{}{
				{
					"path": AddonScriptPath,
					"mode": 0755,
					"contents": map[string]interface{}{
						"source": "data:text/plain;base64," + b64(script),
					},
				},
			},
		},
		"systemd": map[string]interface{}{
			"units": []map[string]interface{}{
				{
					"name":    "kata-addon-kernel.service",
					"enabled": true,
					"contents": `[Unit]
Description=Install Kata kernel from addon image
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=` + AddonScriptPath + `
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target`,
				},
			},
		},
	}

	return json.Marshal(ign)
}

func renderKernelScript() string {
	return `#!/bin/bash
set -euo pipefail

IMAGE="ADDON_IMAGE"
KERNEL="KERNEL_PATH"
DEST="` + AddonDestPath + `"

echo "[INFO] Pulling addon image: ${IMAGE}"
podman pull ${IMAGE}

CTR=$(podman create ${IMAGE})
podman cp ${CTR}:${KERNEL} ${DEST}
podman rm ${CTR}

chmod 0755 ${DEST}
echo "[INFO] Kata addon kernel update complete"
`
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
