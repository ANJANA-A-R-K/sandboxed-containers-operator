package controllers

import (
	"context"
	"encoding/base64"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AddonArtifactsCMName = "kata-addon-artifacts"
	AddonMCName          = "90-kata-addon-artifacts"
)

func boolPtr(b bool) *bool {
	return &b
}

func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC(
	ctx context.Context,
	machinePool string,
) error {

	if r.DeploymentMode != MachineConfigMode {
		return nil
	}

	// Fetch ConfigMap
	cm := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      AddonArtifactsCMName,
		Namespace: r.OperatorNamespace,
	}, cm)

	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("Addon ConfigMap not found, deleting MachineConfig if present")
			return r.deleteAddonArtifactsMC(ctx)
		}
		return err
	}

	addonImage := cm.Data["addonImage"]
	kernelPath := cm.Data["kernelPath"]

	if addonImage == "" || kernelPath == "" {
		return fmt.Errorf("addonImage or kernelPath missing in ConfigMap")
	}

	script := fmt.Sprintf(`#!/bin/bash
set -euo pipefail

IMAGE="%s"
KERNEL_PATH="%s"
DEST="/var/cache/kata-containers/vmlinuz.ibm-se"

echo "[INFO] Pulling addon image: ${IMAGE}"
podman pull ${IMAGE}

echo "[INFO] Extracting kernel from container"
CTR_ID=$(podman create ${IMAGE})
podman cp ${CTR_ID}:${KERNEL_PATH} ${DEST}
podman rm ${CTR_ID}

chmod 0644 ${DEST}
echo "[INFO] Kata addon kernel update complete"
`, addonImage, kernelPath)

	encodedScript := base64.StdEncoding.EncodeToString([]byte(script))

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: AddonMCName,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": machinePool,
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: mcfgv1.Config{
				Ignition: mcfgv1.Ignition{
					Version: "3.2.0",
				},
				Storage: mcfgv1.Storage{
					Files: []mcfgv1.File{
						{
							Node: mcfgv1.Node{
								Path: "/usr/local/bin/update-kata-kernel.sh",
							},
							FileEmbedded1: mcfgv1.FileEmbedded1{
								Contents: mcfgv1.Resource{
									Source: fmt.Sprintf(
										"data:text/plain;base64,%s",
										encodedScript,
									),
								},
								Mode: 0755,
							},
						},
					},
				},
				Systemd: mcfgv1.Systemd{
					Units: []mcfgv1.Unit{
						{
							Name:    "kata-addon-kernel.service",
							Enabled: boolPtr(true),
							Contents: `[Unit]
Description=Update Kata Addon Kernel
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/update-kata-kernel.sh
RemainAfterExit=true

[Install]
WantedBy=multi-user.target
`,
						},
					},
				},
			},
		},
	}

	existing := &mcfgv1.MachineConfig{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: AddonMCName}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("Creating addon MachineConfig")
			return r.Client.Create(ctx, mc)
		}
		return err
	}

	mc.ResourceVersion = existing.ResourceVersion
	r.Log.Info("Updating addon MachineConfig")
	return r.Client.Update(ctx, mc)
}

func (r *KataConfigOpenShiftReconciler) deleteAddonArtifactsMC(
	ctx context.Context,
) error {

	mc := &mcfgv1.MachineConfig{}
	err := r.Client.Get(ctx, client.ObjectKey{Name: AddonMCName}, mc)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	r.Log.Info("Deleting addon MachineConfig")
	return r.Client.Delete(ctx, mc)
}