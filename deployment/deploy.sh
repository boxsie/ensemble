#!/usr/bin/env bash
set -euo pipefail

# Deploy Ensemble to GCE e2-micro via Artifact Registry.
#
# Usage:
#   ./deployment/deploy.sh                           # first-time create
#   ./deployment/deploy.sh --update                  # redeploy new image
#   ./deployment/deploy.sh --project my-proj --zone us-central1-a

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Defaults
PROJECT=""
REGION="europe-west1"
ZONE="europe-west1-b"
VM_NAME="ensemble-seed"
REPO_NAME="ensemble"
IMAGE_NAME="ensemble"
UPDATE=false

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Options:
  --project ID     GCP project ID (required, or set CLOUDSDK_CORE_PROJECT)
  --region REGION  Artifact Registry region (default: ${REGION})
  --zone ZONE      GCE zone (default: ${ZONE})
  --vm-name NAME   VM instance name (default: ${VM_NAME})
  --update         Update existing VM instead of creating new one
  -h, --help       Show this help
EOF
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --project)  PROJECT="$2"; shift 2 ;;
        --region)   REGION="$2"; shift 2 ;;
        --zone)     ZONE="$2"; shift 2 ;;
        --vm-name)  VM_NAME="$2"; shift 2 ;;
        --update)   UPDATE=true; shift ;;
        -h|--help)  usage ;;
        *)          echo "Unknown option: $1"; usage ;;
    esac
done

# Resolve project
if [[ -z "${PROJECT}" ]]; then
    PROJECT="${CLOUDSDK_CORE_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
fi
if [[ -z "${PROJECT}" ]]; then
    echo "Error: no GCP project. Use --project or set CLOUDSDK_CORE_PROJECT"
    exit 1
fi

IMAGE_URI="${REGION}-docker.pkg.dev/${PROJECT}/${REPO_NAME}/${IMAGE_NAME}:latest"

echo "=== Ensemble Deploy ==="
echo "Project:  ${PROJECT}"
echo "Region:   ${REGION}"
echo "Zone:     ${ZONE}"
echo "VM:       ${VM_NAME}"
echo "Image:    ${IMAGE_URI}"
echo "Mode:     $(${UPDATE} && echo 'update' || echo 'create')"
echo ""

# Enable APIs (idempotent)
echo "--- Enabling APIs ---"
gcloud services enable \
    artifactregistry.googleapis.com \
    compute.googleapis.com \
    --project="${PROJECT}" --quiet

# Create Artifact Registry repo (idempotent)
echo "--- Ensuring Artifact Registry repo ---"
gcloud artifacts repositories describe "${REPO_NAME}" \
    --location="${REGION}" \
    --project="${PROJECT}" >/dev/null 2>&1 \
|| gcloud artifacts repositories create "${REPO_NAME}" \
    --repository-format=docker \
    --location="${REGION}" \
    --project="${PROJECT}" \
    --quiet

# Configure docker auth for Artifact Registry
echo "--- Configuring docker auth ---"
gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet

# Build and push locally
echo "--- Building image locally ---"
docker build -f "${SCRIPT_DIR}/Dockerfile" -t "${IMAGE_URI}" "${PROJECT_ROOT}"

echo "--- Pushing image ---"
docker push "${IMAGE_URI}"

# Startup script that pulls and runs the container on boot.
STARTUP_SCRIPT="$(cat <<SCRIPT
#!/bin/bash
set -e

# COS has a read-only root filesystem — docker-credential-gcr needs a writable HOME.
export HOME=/var/tmp

# Authenticate docker with Artifact Registry via VM service account.
docker-credential-gcr configure-docker --registries=${REGION}-docker.pkg.dev

# Pull latest image.
docker pull ${IMAGE_URI}

# Stop existing container if running.
docker stop ensemble 2>/dev/null || true
docker rm ensemble 2>/dev/null || true

# Run container with persistent data volume (UID 1000 = ensemble user in container).
mkdir -p /home/ensemble/data
chown 1000:1000 /home/ensemble/data
docker run -d \
    --name ensemble \
    --restart unless-stopped \
    -v /home/ensemble/data:/data \
    ${IMAGE_URI}
SCRIPT
)"

if ${UPDATE}; then
    # Update: push new startup script and re-run it via SSH.
    echo "--- Updating VM ---"
    gcloud compute instances add-metadata "${VM_NAME}" \
        --zone="${ZONE}" \
        --project="${PROJECT}" \
        --metadata=startup-script="${STARTUP_SCRIPT}"

    echo "--- Restarting container ---"
    gcloud compute ssh "${VM_NAME}" \
        --zone="${ZONE}" \
        --project="${PROJECT}" \
        --command="sudo bash -c 'export HOME=/var/tmp && docker-credential-gcr configure-docker --registries=${REGION}-docker.pkg.dev && docker stop ensemble 2>/dev/null; docker rm ensemble 2>/dev/null; docker pull ${IMAGE_URI} && mkdir -p /home/ensemble/data && chown 1000:1000 /home/ensemble/data && docker run -d --name ensemble --restart unless-stopped -v /home/ensemble/data:/data ${IMAGE_URI}'"
else
    # Create firewall rule for gRPC (idempotent)
    echo "--- Ensuring firewall rule ---"
    gcloud compute firewall-rules describe allow-ensemble-grpc \
        --project="${PROJECT}" >/dev/null 2>&1 \
    || gcloud compute firewall-rules create allow-ensemble-grpc \
        --project="${PROJECT}" \
        --allow=tcp:9090 \
        --target-tags=ensemble-seed \
        --source-ranges=0.0.0.0/0 \
        --quiet

    # Create VM with COS image and startup script.
    echo "--- Creating VM ---"
    gcloud compute instances create "${VM_NAME}" \
        --project="${PROJECT}" \
        --zone="${ZONE}" \
        --machine-type=e2-micro \
        --image-family=cos-stable \
        --image-project=cos-cloud \
        --tags=ensemble-seed \
        --scopes=storage-ro \
        --metadata=startup-script="${STARTUP_SCRIPT}" \
        --quiet
fi

echo ""
echo "=== Deploy complete ==="
echo "SSH:   gcloud compute ssh ${VM_NAME} --zone=${ZONE} --project=${PROJECT}"
echo "Logs:  gcloud compute ssh ${VM_NAME} --zone=${ZONE} --project=${PROJECT} -- docker logs ensemble"
echo "Shell: gcloud compute ssh ${VM_NAME} --zone=${ZONE} --project=${PROJECT} -- docker exec -it ensemble sh"
