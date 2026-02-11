package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AddonArtifactsCM = "kata-addon-artifacts"
	AddonMCName      = "99-kata-addon-kernel"
)

func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC(machinePool string) error {

	cm := &corev1.ConfigMap{}
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Name:      AddonArtifactsCM,
		Namespace: OperatorNamespace,
	}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("Addon CM not found, skipping kernel addon")
			return nil
		}
		return err
	}

	addonImage := cm.Data["addonImage"]
	kernelPath := cm.Data["kernelPath"]

	if addonImage == "" || kernelPath == "" {
		r.Log.Info("addonImage or kernelPath missing in CM, skipping kernel addon")
		return nil
	}

	r.Log.Info("Reconciling addon MachineConfig",
		"addonImage", addonImage,
		"kernelPath", kernelPath,
	)

	ignitionJSON, err := generateIgnitionJSON(addonImage, kernelPath)
	if err != nil {
		return fmt.Errorf("failed to generate ignition JSON: %w", err)
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: AddonMCName,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": machinePool,
				"app":                                    "cluster-kataconfig",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: k8sruntime.RawExtension{
				Raw: ignitionJSON,
			},
		},
	}

	err = r.Client.Create(context.TODO(), mc)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			existing := &mcfgv1.MachineConfig{}
			if err := r.Client.Get(context.TODO(), client.ObjectKey{Name: AddonMCName}, existing); err != nil {
				return err
			}
			existing.Spec = mc.Spec
			return r.Client.Update(context.TODO(), existing)
		}
		return err
	}

	r.Log.Info("Addon MachineConfig created successfully")
	return nil
}

func generateIgnitionJSON(addonImage, kernelPath string) ([]byte, error) {
	script := renderKernelScript()

	script = strings.ReplaceAll(script, "ADDON_IMAGE", addonImage)
	script = strings.ReplaceAll(script, "KERNEL_PATH", kernelPath)

	ign := map[string]interface{}{
		"ignition": map[string]interface{}{
			"version": "3.2.0",
		},
		"storage": map[string]interface{}{
			"files": []map[string]interface{}{
				{
					"path": "/usr/local/bin/update-kata-kernel.sh",
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
ExecStart=/usr/local/bin/update-kata-kernel.sh
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
KERNEL_PATH="KERNEL_PATH"
DEST="/var/cache/kata-containers/vmlinuz.ibm-se"

echo "[INFO] Pulling addon image: ${IMAGE}"
podman pull ${IMAGE}

echo "[INFO] Extracting kernel from container"
CTR_ID=$(podman create ${IMAGE})
podman cp ${CTR_ID}:${KERNEL_PATH} ${DEST}
podman rm ${CTR_ID}

chmod 0644 ${DEST}
echo "[INFO] Kata addon kernel update complete"
`
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}