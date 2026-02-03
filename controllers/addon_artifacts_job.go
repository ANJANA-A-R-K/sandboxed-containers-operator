package controllers

import (
	"context"
	"fmt"
	"runtime"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AddonArtifactsCMName = "kata-addon-artifacts"
	AddonJobPrefix       = "kata-addon-artifacts"
)

// reconcileAddonArtifactsJob installs Kata addon kernel via Job (MachineConfigMode only)
func (r *KataConfigOpenShiftReconciler) reconcileAddonArtifactsJob(
	ctx context.Context,
) error {

	// Only for s390x
	if runtime.GOARCH != "s390x" {
		r.Log.Info("Skipping addon artifacts: not s390x")
		return nil
	}

	// Only in MachineConfigMode
	// if r.DeploymentMode != "MachineConfigMode" {
	// 	r.Log.Info("Skipping addon artifacts: not MachineConfigMode")
	// 	return nil
	// }

	// Fetch addon ConfigMap
	cm := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      AddonArtifactsCMName,
		Namespace: OperatorNamespace,
	}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("Addon artifacts ConfigMap not found, skipping")
			return nil
		}
		return err
	}

	addonImage := cm.Data["addonImage"]
	kernelPath := cm.Data["kernelPath"]

	if addonImage == "" || kernelPath == "" {
		r.Log.Info("addonImage or kernelPath missing, skipping")
		return nil
	}

	// List nodes
	nodes := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodes); err != nil {
		return err
	}

	for _, node := range nodes.Items {
		if node.Labels["kubernetes.io/arch"] != "s390x" {
			continue
		}

		if err := r.ensureAddonJobForNode(
			ctx,
			node.Name,
			addonImage,
			kernelPath,
		); err != nil {
			return err
		}
	}

	return nil
}

func (r *KataConfigOpenShiftReconciler) ensureAddonJobForNode(
	ctx context.Context,
	nodeName, addonImage, kernelPath string,
) error {

	jobName := fmt.Sprintf("%s-%s", AddonJobPrefix, nodeName)

	job := &batchv1.Job{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      jobName,
		Namespace: OperatorNamespace,
	}, job)

	if err == nil {
		// Job already exists → do nothing
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	newJob := buildAddonArtifactsJob(
		jobName,
		nodeName,
		addonImage,
		kernelPath,
	)

	r.Log.Info("Creating addon artifacts job",
		"node", nodeName,
		"job", jobName,
	)

	return r.Client.Create(ctx, newJob)
}

func buildAddonArtifactsJob(
	name, nodeName, addonImage, kernelPath string,
) *batchv1.Job {

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: OperatorNamespace,
			Labels: map[string]string{
				"app": "kata-addon-artifacts",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: int32Ptr(2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeName:      nodeName,
					RestartPolicy: corev1.RestartPolicyOnFailure,
					HostPID:       true,
					Containers: []corev1.Container{
						{
							Name:  "installer",
							Image: r.OperatorImage, // operator image contains skopeo
							Command: []string{
								"/bin/bash",
								"-c",
							},
							Args: []string{
								buildInstallerScript(),
							},
							Env: []corev1.EnvVar{
								{Name: "ADDON_IMAGE", Value: addonImage},
								{Name: "ADDON_KERNEL_PATH", Value: kernelPath},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: boolPtr(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "host",
									MountPath: "/host",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "host",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildInstallerScript() string {
	return `
set -euo pipefail

echo "[INFO] Starting Kata addon kernel installation"

ADDON_IMAGE="${ADDON_IMAGE:?}"
KERNEL_SRC="${ADDON_KERNEL_PATH:?}"

INSTALL_DIR="/host/etc/kata-containers"
CFG="${INSTALL_DIR}/kata-se/configuration.toml"
TMP="/tmp/kata-addon-$$"

mkdir -p "${INSTALL_DIR}" "${TMP}"

echo "[INFO] Pulling addon image: ${ADDON_IMAGE}"
skopeo copy docker://${ADDON_IMAGE} dir:${TMP}

KERNEL_FILE="${TMP}${KERNEL_SRC}"

if [ ! -f "${KERNEL_FILE}" ]; then
  echo "[ERROR] Kernel not found at ${KERNEL_FILE}"
  exit 1
fi

KERNEL_NAME="$(basename ${KERNEL_SRC})"
cp "${KERNEL_FILE}" "${INSTALL_DIR}/${KERNEL_NAME}"
chmod 0644 "${INSTALL_DIR}/${KERNEL_NAME}"

echo "[INFO] Kernel installed at ${INSTALL_DIR}/${KERNEL_NAME}"

if [ -f "${CFG}" ]; then
  sed -i "s|^kernel *= *\".*\"|kernel = \"${INSTALL_DIR}/${KERNEL_NAME}\"|" "${CFG}"
  echo "[INFO] configuration.toml updated"
else
  echo "[WARN] configuration.toml not found"
fi

rm -rf "${TMP}"

echo "[INFO] Kata addon kernel installation complete"
`
}

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool   { return &v }
