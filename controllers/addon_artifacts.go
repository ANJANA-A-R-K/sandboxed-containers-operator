package controllers

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AddonArtifactsCM = "kata-addon-artifacts"
	AddonMCName      = "99-kata-addon-kernel"
)

// reconcileAddonArtifactsMC creates a MachineConfig that installs the kernel addon on s390x workers
func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC() error {
	if runtime.GOARCH != "s390x" {
		r.Log.Info("Skipping addon MC: architecture is not s390x")
		return nil
	}

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

	r.Log.Info("Creating/updating addon MC for kernel installation")

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: AddonMCName,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": "worker",
				"app":                                    "cluster-kataconfig",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: generateAddonIgnition(addonImage, kernelPath),
		},
	}

	// Create or update MC
	err = r.Client.Create(context.TODO(), mc)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			existingMC := &mcfgv1.MachineConfig{}
			if err := r.Client.Get(context.TODO(), client.ObjectKey{Name: AddonMCName}, existingMC); err != nil {
				return err
			}
			existingMC.Spec = mc.Spec
			return r.Client.Update(context.TODO(), existingMC)
		}
		return err
	}

	r.Log.Info("Addon MC created/updated successfully")
	return nil
}

// generateAddonIgnition generates a MachineConfig Ignition spec with embedded kernel install script
func generateAddonIgnition(addonImage, kernelPath string) mcfgv1.Ignition {
	return mcfgv1.Ignition{
		Version: "3.2.0",
		Storage: mcfgv1.Storage{
			Files: []mcfgv1.File{
				{
					Node: mcfgv1.Node{
						Path: "/usr/local/bin/update-kata-kernel.sh",
					},
					FileEmbedded1: mcfgv1.FileEmbedded1{
						Contents: mcfgv1.Resource{
							Source: ptr("data:text/plain;base64," + b64(renderKernelScript(addonImage, kernelPath))),
						},
						Mode: ptrInt(0755),
					},
				},
			},
		},
		Systemd: mcfgv1.Systemd{
			Units: []mcfgv1.Unit{
				{
					Name:    "kata-addon-kernel.service",
					Enabled: ptr(true),
					Contents: ptr(`[Unit]
Description=Install Kata kernel from addon image
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/update-kata-kernel.sh

[Install]
WantedBy=multi-user.target`),
				},
			},
		},
	}
}

// renderKernelScript generates a bash script that pulls the kernel from container and updates TOML
func renderKernelScript(addonImage, kernelSrc string) string {
	return fmt.Sprintf(`#!/bin/bash
set -e

# Directories
INSTALL_DIR="/etc/kata-containers"
TEMP_DIR="/tmp/kata-addon-$$"

mkdir -p "$INSTALL_DIR" "$TEMP_DIR"

echo "Pulling kernel from container image: %s"
ctr -n=k8s.io images pull "%s"
ctr -n=k8s.io images export "$TEMP_DIR/addon.tar" "%s"
tar -xf "$TEMP_DIR/addon.tar" -C "$TEMP_DIR" "%s"

KERNEL_FILE=$(basename "%s")
cp "$TEMP_DIR/%s" "$INSTALL_DIR/"
chmod 644 "$INSTALL_DIR/$KERNEL_FILE"

# Update configuration.toml
CFG="/etc/kata-containers/kata-se/configuration.toml"
if [ -f "$CFG" ]; then
    sed -i "s|^kernel *= *\".*\"|kernel = \"/etc/kata-containers/$KERNEL_FILE\"|" "$CFG"
    echo "Updated Kata kernel path in configuration.toml"
else
    echo "Warning: $CFG not found, skipping config update"
fi

rm -rf "$TEMP_DIR"
echo "Kernel addon installation completed"
`, addonImage, addonImage, addonImage, kernelSrc, kernelSrc, kernelSrc)
}

func ptr(s string) *string {
	return &s
}

func ptrInt(i int) *int {
	return &i
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
