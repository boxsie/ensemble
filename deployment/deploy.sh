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
    cloudbuild.googleapis.com \
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

# Build and push via Cloud Build
echo "--- Building image via Cloud Build ---"
gcloud builds submit "${PROJECT_ROOT}" \
    --tag="${IMAGE_URI}" \
    --project="${PROJECT}" \
    --quiet

if ${UPDATE}; then
    # Update existing VM
    echo "--- Updating VM container ---"
    gcloud compute instances update-container "${VM_NAME}" \
        --zone="${ZONE}" \
        --project="${PROJECT}" \
        --container-image="${IMAGE_URI}"
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

    # Create VM with container
    echo "--- Creating VM ---"
    gcloud compute instances create-with-container "${VM_NAME}" \
        --project="${PROJECT}" \
        --zone="${ZONE}" \
        --machine-type=e2-micro \
        --container-image="${IMAGE_URI}" \
        --container-mount-host-path=mount-path=/data,host-path=/home/ensemble/data,mode=rw \
        --tags=ensemble-seed \
        --quiet
fi

echo ""
echo "=== Deploy complete ==="
echo "SSH:  gcloud compute ssh ${VM_NAME} --zone=${ZONE} --project=${PROJECT}"
echo "Logs: gcloud compute ssh ${VM_NAME} --zone=${ZONE} --project=${PROJECT} -- docker logs \$(docker ps -q)"
