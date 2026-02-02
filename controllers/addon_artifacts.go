package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"

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

// reconcileAddonArtifactsMC creates a MachineConfig that installs the kernel addon on s390x nodes
func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC() error {
	if runtime.GOARCH != "s390x" {
		r.Log.Info("Skipping addon MC: architecture is not s390x")
		return nil
	}

	// Fetch ConfigMap containing addon info
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

	ignitionJSON, err := generateIgnitionJSON(addonImage, kernelPath)
	if err != nil {
		return fmt.Errorf("failed to generate ignition JSON: %v", err)
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: AddonMCName,
			Labels: map[string]string{
				// On SNO, node is master role; on multi-node, change to worker as needed
				"machineconfiguration.openshift.io/role": "master",
				"app":                                    "cluster-kataconfig",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: k8sruntime.RawExtension{
				Raw: ignitionJSON,
			},
		},
	}

	// Create or update the MC
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

// generateIgnitionJSON returns raw JSON bytes for MachineConfig
func generateIgnitionJSON(addonImage, kernelPath string) ([]byte, error) {
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
						"source": "data:text/plain;base64," + b64(renderKernelScript(addonImage, kernelPath)),
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
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/update-kata-kernel.sh

[Install]
WantedBy=multi-user.target`,
				},
			},
		},
	}

	return json.Marshal(ign)
}

// renderKernelScript returns a shell script that pulls kernel using skopeo/umoci and updates configuration.toml
func renderKernelScript(addonImage, kernelSrc string) string {
	return fmt.Sprintf(`#!/bin/bash
set -ex

INSTALL_DIR="/etc/kata-containers"
TEMP_DIR="/tmp/kata-addon-$$"

mkdir -p "$INSTALL_DIR" "$TEMP_DIR"

echo "$(date '+%%F %%T') Extracting kernel from container image: %s"

# Verify required commands
command -v skopeo >/dev/null 2>&1 || { echo "skopeo not installed"; exit 1; }
command -v umoci >/dev/null 2>&1 || { echo "umoci not installed"; exit 1; }

# Copy image and extract kernel
skopeo copy docker://%s oci:$TEMP_DIR:image
mkdir -p "$TEMP_DIR/extract"
umoci unpack --rootless --image "$TEMP_DIR/image" "$TEMP_DIR/extract"
cp "$TEMP_DIR/extract/%s" "$INSTALL_DIR/"
chmod 644 "$INSTALL_DIR/$(basename %s)"

# Update configuration.toml
CFG="/etc/kata-containers/kata-se/configuration.toml"
if [ -f "$CFG" ]; then
    sed -i "s|^kernel *= *\".*\"|kernel = \"/etc/kata-containers/$(basename %s)\"|" "$CFG"
    echo "$(date '+%%F %%T') Updated Kata kernel path in configuration.toml"
else
    echo "$(date '+%%F %%T') Warning: $CFG not found, skipping config update"
fi

rm -rf "$TEMP_DIR"
echo "$(date '+%%F %%T') Kernel addon installation completed"
`, addonImage, addonImage, kernelSrc, kernelSrc, kernelSrc)
}

// ptr helper
func ptr(s string) *string {
	return &s
}

// b64 helper
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// package controllers

// import (
// 	"context"
// 	"encoding/base64"
// 	"encoding/json"
// 	"fmt"
// 	"runtime"

// 	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
// 	corev1 "k8s.io/api/core/v1"
// 	"k8s.io/apimachinery/pkg/api/errors"
// 	k8sruntime "k8s.io/apimachinery/pkg/runtime"
// 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
// 	"sigs.k8s.io/controller-runtime/pkg/client"
// )

// const (
// 	AddonArtifactsCM = "kata-addon-artifacts"
// 	AddonMCName      = "99-kata-addon-kernel"
// )

// // reconcileAddonArtifactsMC creates a MachineConfig that installs the kernel addon on s390x workers
// func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC() error {
// 	if runtime.GOARCH != "s390x" {
// 		r.Log.Info("Skipping addon MC: architecture is not s390x")
// 		return nil
// 	}

// 	cm := &corev1.ConfigMap{}
// 	err := r.Client.Get(context.TODO(), client.ObjectKey{
// 		Name:      AddonArtifactsCM,
// 		Namespace: OperatorNamespace,
// 	}, cm)
// 	if err != nil {
// 		if errors.IsNotFound(err) {
// 			r.Log.Info("Addon CM not found, skipping kernel addon")
// 			return nil
// 		}
// 		return err
// 	}

// 	addonImage := cm.Data["addonImage"]
// 	kernelPath := cm.Data["kernelPath"]

// 	if addonImage == "" || kernelPath == "" {
// 		r.Log.Info("addonImage or kernelPath missing in CM, skipping kernel addon")
// 		return nil
// 	}

// 	r.Log.Info("Creating/updating addon MC for kernel installation")

// 	ignitionJSON, err := generateIgnitionJSON(addonImage, kernelPath)
// 	if err != nil {
// 		return fmt.Errorf("failed to generate ignition JSON: %v", err)
// 	}

// 	mc := &mcfgv1.MachineConfig{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name: AddonMCName,
// 			Labels: map[string]string{
// 				"machineconfiguration.openshift.io/role": "master",
// 				"app":                                    "cluster-kataconfig",
// 			},
// 		},
// 		Spec: mcfgv1.MachineConfigSpec{
// 			Config: k8sruntime.RawExtension{
// 				Raw: ignitionJSON,
// 			},
// 		},
// 	}

// 	// Create or update the MC
// 	err = r.Client.Create(context.TODO(), mc)
// 	if err != nil {
// 		if errors.IsAlreadyExists(err) {
// 			existingMC := &mcfgv1.MachineConfig{}
// 			if err := r.Client.Get(context.TODO(), client.ObjectKey{Name: AddonMCName}, existingMC); err != nil {
// 				return err
// 			}
// 			existingMC.Spec = mc.Spec
// 			return r.Client.Update(context.TODO(), existingMC)
// 		}
// 		return err
// 	}

// 	r.Log.Info("Addon MC created/updated successfully")
// 	return nil
// }

// // generateIgnitionJSON returns raw JSON bytes for MachineConfig
// func generateIgnitionJSON(addonImage, kernelPath string) ([]byte, error) {
// 	ign := map[string]interface{}{
// 		"ignition": map[string]interface{}{
// 			"version": "3.2.0",
// 		},
// 		"storage": map[string]interface{}{
// 			"files": []map[string]interface{}{
// 				{
// 					"path": "/usr/local/bin/update-kata-kernel.sh",
// 					"mode": 0755,
// 					"contents": map[string]interface{}{
// 						"source": "data:text/plain;base64," + b64(renderKernelScript(addonImage, kernelPath)),
// 					},
// 				},
// 			},
// 		},
// 		"systemd": map[string]interface{}{
// 			"units": []map[string]interface{}{
// 				{
// 					"name":    "kata-addon-kernel.service",
// 					"enabled": true,
// 					"contents": `[Unit]
// Description=Install Kata kernel from addon image
// After=network.target

// [Service]
// Type=oneshot
// ExecStart=/usr/local/bin/update-kata-kernel.sh

// [Install]
// WantedBy=multi-user.target`,
// 				},
// 			},
// 		},
// 	}

// 	return json.Marshal(ign)
// }

// // renderKernelScript returns a shell script that pulls kernel using skopeo and updates configuration.toml
// func renderKernelScript(addonImage, kernelSrc string) string {
// 	return fmt.Sprintf(`#!/bin/bash
// set -e

// INSTALL_DIR="/etc/kata-containers"
// TEMP_DIR="/tmp/kata-addon-$$"

// mkdir -p "$INSTALL_DIR" "$TEMP_DIR"

// echo "Extracting kernel from container image: %s"

// # Use skopeo to copy image and extract kernel
// skopeo copy docker://%s oci:$TEMP_DIR:image
// mkdir -p "$TEMP_DIR/extract"
// umoci unpack --rootless --image "$TEMP_DIR/image" "$TEMP_DIR/extract"
// cp "$TEMP_DIR/extract/%s" "$INSTALL_DIR/"
// chmod 644 "$INSTALL_DIR/$(basename %s)"

// # Update configuration.toml
// CFG="/etc/kata-containers/kata-se/configuration.toml"
// if [ -f "$CFG" ]; then
//     sed -i "s|^kernel *= *\".*\"|kernel = \"/etc/kata-containers/$(basename %s)\"|" "$CFG"
//     echo "Updated Kata kernel path in configuration.toml"
// else
//     echo "Warning: $CFG not found, skipping config update"
// fi

// rm -rf "$TEMP_DIR"
// echo "Kernel addon installation completed"
// `, addonImage, addonImage, kernelSrc, kernelSrc, kernelSrc)
// }

// func ptr(s string) *string {
// 	return &s
// }

// func b64(s string) string {
// 	return base64.StdEncoding.EncodeToString([]byte(s))
// }
