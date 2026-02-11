#!/bin/bash
set -euo pipefail

IMAGE="{{ADDON_IMAGE}}"
KERNEL_PATH="{{KERNEL_PATH}}"

DEST="/var/cache/kata-containers/vmlinuz.ibm-se"

TMP_DIR=$(mktemp -d)

echo "Pulling addon image..."
podman pull ${IMAGE}

echo "Extracting kernel..."
podman create --name kata-addon ${IMAGE}
podman cp kata-addon:${KERNEL_PATH} ${DEST}
podman rm kata-addon

chmod 0644 ${DEST}
echo "Addon kernel updated successfully."
