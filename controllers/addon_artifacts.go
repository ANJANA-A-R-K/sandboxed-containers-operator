package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
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

// reconcileAddonArtifactsMC creates a MachineConfig that installs the kernel addon on s390x nodes
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

	r.Log.Info("Creating/updating addon MachineConfig",
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
				"machineconfiguration.openshift.io/role": "master", // SNO-safe
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

	r.Log.Info("Addon MachineConfig created/updated successfully")
	return nil
}

// generateIgnitionJSON builds the ignition config
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

// renderKernelScript returns a skopeo-only kernel extraction script
func renderKernelScript() string {
	return `#!/bin/bash
exec > >(systemd-cat -t kata-addon-kernel) 2>&1
set -euxo pipefail

INSTALL_DIR="/etc/kata-containers"
TEMP_DIR="/tmp/kata-addon-$$"
IMAGE_REF="addon"

mkdir -p "${INSTALL_DIR}" "${TEMP_DIR}"

echo "[$(date)] Pulling addon image via skopeo"

command -v skopeo >/dev/null 2>&1 || {
  echo "ERROR: skopeo not installed"
  exit 1
}

skopeo copy docker://ADDON_IMAGE oci:${TEMP_DIR}:${IMAGE_REF}

echo "[$(date)] Searching kernel in OCI layers"

FOUND_KERNEL=""

for layer in ${TEMP_DIR}/blobs/sha256/*; do
  if tar -tf "$layer" | grep -q "KERNEL_PATH"; then
    echo "Found kernel in layer $layer"
    tar -xf "$layer" -C "${TEMP_DIR}" "KERNEL_PATH"
    FOUND_KERNEL="${TEMP_DIR}/KERNEL_PATH"
    break
  fi
done

if [ -z "$FOUND_KERNEL" ]; then
  echo "ERROR: kernel not found in addon image"
  exit 1
fi

KERNEL_NAME="$(basename KERNEL_PATH)"
cp "${FOUND_KERNEL}" "${INSTALL_DIR}/${KERNEL_NAME}"
chmod 0644 "${INSTALL_DIR}/${KERNEL_NAME}"

CFG="/etc/kata-containers/kata-se/configuration.toml"
if [ -f "$CFG" ]; then
  sed -i "s|^kernel *= *\".*\"|kernel = \"${INSTALL_DIR}/${KERNEL_NAME}\"|" "$CFG"
  echo "[$(date)] Updated Kata configuration.toml"
else
  echo "WARNING: $CFG not found"
fi

rm -rf "${TEMP_DIR}"
echo "[$(date)] Kata addon kernel installation completed successfully"
`
}

// base64 helper
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

// // reconcileAddonArtifactsMC creates a MachineConfig that installs the kernel addon on s390x nodes
// func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsMC() error {
// 	if runtime.GOARCH != "s390x" {
// 		r.Log.Info("Skipping addon MC: architecture is not s390x")
// 		return nil
// 	}

// 	// Fetch ConfigMap containing addon info
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
// 				// On SNO, node is master role; on multi-node, change to worker as needed
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

// // renderKernelScript returns a shell script that pulls kernel using skopeo/umoci and updates configuration.toml
// func renderKernelScript(addonImage, kernelSrc string) string {
// 	return fmt.Sprintf(`#!/bin/bash
// set -ex

// INSTALL_DIR="/etc/kata-containers"
// TEMP_DIR="/tmp/kata-addon-$$"

// mkdir -p "$INSTALL_DIR" "$TEMP_DIR"

// echo "$(date '+%%F %%T') Extracting kernel from container image: %s"

// # Verify required commands
// command -v skopeo >/dev/null 2>&1 || { echo "skopeo not installed"; exit 1; }
// command -v umoci >/dev/null 2>&1 || { echo "umoci not installed"; exit 1; }

// # Copy image and extract kernel
// skopeo copy docker://%s oci:$TEMP_DIR:image
// mkdir -p "$TEMP_DIR/extract"
// umoci unpack --rootless --image "$TEMP_DIR/image" "$TEMP_DIR/extract"
// cp "$TEMP_DIR/extract/%s" "$INSTALL_DIR/"
// chmod 644 "$INSTALL_DIR/$(basename %s)"

// # Update configuration.toml
// CFG="/etc/kata-containers/kata-se/configuration.toml"
// if [ -f "$CFG" ]; then
//     sed -i "s|^kernel *= *\".*\"|kernel = \"/etc/kata-containers/$(basename %s)\"|" "$CFG"
//     echo "$(date '+%%F %%T') Updated Kata kernel path in configuration.toml"
// else
//     echo "$(date '+%%F %%T') Warning: $CFG not found, skipping config update"
// fi

// rm -rf "$TEMP_DIR"
// echo "$(date '+%%F %%T') Kernel addon installation completed"
// `, addonImage, addonImage, kernelSrc, kernelSrc, kernelSrc)
// }

// // ptr helper
// func ptr(s string) *string {
// 	return &s
// }

// // b64 helper
// func b64(s string) string {
// 	return base64.StdEncoding.EncodeToString([]byte(s))
// }

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
