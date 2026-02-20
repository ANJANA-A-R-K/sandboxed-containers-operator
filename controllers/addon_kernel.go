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
	"sigs.k8s.io/controller-runtime/pkg/client"    
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	AddonArtifactsCM = "kata-addon-artifacts"
	AddonMCName      = "99-kata-addon-kernel"
	AddonDestPath    = "/var/cache/kata-containers/vmlinuz.ibm-se"
	AddonScriptPath  = "/usr/local/bin/update-kata-kernel.sh"
)

func (r *KataConfigOpenShiftReconciler) CreateOrUpdateAddonKernelMC(machinePool string) error {
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
	kataVersionPath := strings.TrimSpace(cm.Data["kataVersion"])
	if addonImage == "" || kernelPath == "" || kataVersionPath == ""{
		r.Log.Info("addonImage or kernelPath missing, skipping addon MC")
		return nil
	}

	r.Log.Info("Reconciling addon MachineConfig", "image", addonImage, "kernelPath", kernelPath)

	ignJSON, err := generateIgnitionJSON(addonImage, kernelPath, kataVersionPat)
	if err != nil {
		return fmt.Errorf("failed to generate ignition JSON: %w", err)
	}

    mc := &mcfgv1.MachineConfig{
        ObjectMeta: metav1.ObjectMeta{
            Name: AddonMCName,
        },
    }

    op, err := controllerutil.CreateOrUpdate(ctx, r.Client, mc, func() error {
        if mc.Labels == nil {
            mc.Labels = map[string]string{}
        }
        mc.Labels["machineconfiguration.openshift.io/role"] = machinePool
        mc.Labels["app"] = "sandboxed-containers-addon"

        mc.Spec = mcfgv1.MachineConfigSpec{
            Config: runtime.RawExtension{Raw: ignJSON},
        }
        return nil
    })
    if err != nil {
        return err
    }

    r.Log.Info("addon MachineConfig reconciled", "operation", op)
    return nil
}

func (r *KataConfigOpenShiftReconciler) DeleteAddonKernelMC() error {
	ctx := context.TODO()    
	r.Log.Info("Deleting addon MC") 
	mc := &mcfgv1.MachineConfig{        
		ObjectMeta: metav1.ObjectMeta{            
			Name: AddonMCName,        
		},   
	}    
	return client.IgnoreNotFound(r.Client.Delete(ctx, mc))
}

func generateIgnitionJSON(addonImage, kernelPath, kataVersionPath string) ([]byte, error) {
	script := renderKernelScript()
	script = strings.ReplaceAll(script, "ADDON_IMAGE", addonImage)
	script = strings.ReplaceAll(script, "KERNEL_PATH", kernelPath)
	script = strings.ReplaceAll(script, "KATA_VERSION_PATH", kataVersionPath)

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
VERSION_JSON="KATA_VERSION_PATH"
DEST="` + AddonDestPath + `"

TMPDIR=$(mktemp -d)
trap "rm -rf ${TMPDIR}" EXIT

echo "[INFO] Pulling addon image: ${IMAGE}"
podman pull ${IMAGE}

CTR=$(podman create ${IMAGE})

echo "[INFO] Extracting version.json from image"
podman cp ${CTR}:${VERSION_JSON} ${TMPDIR}/version.json

podman cp ${CTR}:${KERNEL} ${DEST}
podman rm ${CTR}

# Extract required kata version from JSON
REQUIRED_VERSION=$(jq -r '.version' ${TMPDIR}/version.json)

if [ -z "${REQUIRED_VERSION}" ]; then
    echo "[ERROR] Could not determine required Kata version from addon image"
    exit 1
fi

# Get installed kata version
INSTALLED_VERSION=$(rpm -q --qf '%{VERSION}' kata-containers 2>/dev/null || true)

if [ -z "${INSTALLED_VERSION}" ]; then
    echo "[ERROR] Kata containers not installed on node"
    exit 1
fi

echo "[INFO] Required Kata version: ${REQUIRED_VERSION}"
echo "[INFO] Installed Kata version: ${INSTALLED_VERSION}"

if [ "${REQUIRED_VERSION}" != "${INSTALLED_VERSION}" ]; then
    echo "[ERROR] Kata version mismatch!"
    echo "[ERROR] Add-on image requires Kata ${REQUIRED_VERSION}"
    echo "[ERROR] Installed Kata version is ${INSTALLED_VERSION}"
    exit 1
fi

chmod 0755 ${DEST}
echo "[INFO] Kata addon kernel update complete"
`
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
