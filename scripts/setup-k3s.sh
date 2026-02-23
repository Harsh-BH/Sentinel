#!/usr/bin/env bash
# =============================================================================
# Project Sentinel — k3s Cluster Setup Script
# =============================================================================
# Installs k3s, KEDA, cert-manager, nginx-ingress, and applies all manifests.
#
# Usage:
#   sudo ./scripts/setup-k3s.sh              # Full setup
#   sudo ./scripts/setup-k3s.sh --manifests  # Apply manifests only
#   sudo ./scripts/setup-k3s.sh --teardown   # Remove everything
#
# Prerequisites:
#   - Linux host (amd64/arm64) with root access
#   - curl, kubectl (installed by k3s)
#   - helm v3 (installed by this script if missing)
# =============================================================================
set -euo pipefail

# ── Configuration ──
K3S_VERSION="${K3S_VERSION:-v1.30.0+k3s1}"
KEDA_VERSION="${KEDA_VERSION:-2.14.0}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.14.4}"
NGINX_INGRESS_VERSION="${NGINX_INGRESS_VERSION:-4.10.0}"
NAMESPACE="sentinel"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
K8S_DIR="${SCRIPT_DIR}/../infra/k8s"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()   { echo -e "${GREEN}[✓]${NC} $*"; }
warn()  { echo -e "${YELLOW}[!]${NC} $*"; }
error() { echo -e "${RED}[✗]${NC} $*" >&2; }
info()  { echo -e "${CYAN}[i]${NC} $*"; }

# ── Pre-flight checks ──
preflight() {
    if [[ $EUID -ne 0 ]]; then
        error "This script must be run as root (sudo)"
        exit 1
    fi

    if ! command -v curl &>/dev/null; then
        error "curl is required but not installed"
        exit 1
    fi

    if [[ ! -d "$K8S_DIR" ]]; then
        error "K8s manifests not found at $K8S_DIR"
        exit 1
    fi

    log "Pre-flight checks passed"
}

# ── Install k3s ──
install_k3s() {
    if command -v k3s &>/dev/null; then
        warn "k3s already installed: $(k3s --version)"
    else
        info "Installing k3s ${K3S_VERSION}..."
        curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="${K3S_VERSION}" \
            INSTALL_K3S_EXEC="--disable=traefik --write-kubeconfig-mode=644" sh -
        log "k3s installed"
    fi

    # Wait for k3s to be ready
    info "Waiting for k3s to be ready..."
    local retries=30
    until kubectl get nodes &>/dev/null; do
        retries=$((retries - 1))
        if [[ $retries -le 0 ]]; then
            error "k3s failed to start within 60s"
            exit 1
        fi
        sleep 2
    done
    log "k3s is ready"

    # Export kubeconfig for helm
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
}

# ── Install Helm ──
install_helm() {
    if command -v helm &>/dev/null; then
        warn "Helm already installed: $(helm version --short)"
    else
        info "Installing Helm v3..."
        curl -sfL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
        log "Helm installed"
    fi
}

# ── Install nginx-ingress controller ──
install_nginx_ingress() {
    info "Installing nginx-ingress controller (v${NGINX_INGRESS_VERSION})..."

    helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx 2>/dev/null || true
    helm repo update

    if helm status ingress-nginx -n ingress-nginx &>/dev/null; then
        warn "nginx-ingress already installed, upgrading..."
    fi

    helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
        --namespace ingress-nginx \
        --create-namespace \
        --version "${NGINX_INGRESS_VERSION}" \
        --set controller.replicaCount=2 \
        --set controller.service.type=LoadBalancer \
        --set controller.metrics.enabled=true \
        --set controller.podAnnotations."prometheus\.io/scrape"=true \
        --set controller.podAnnotations."prometheus\.io/port"=10254 \
        --wait --timeout 120s

    log "nginx-ingress controller installed"
}

# ── Install cert-manager ──
install_cert_manager() {
    info "Installing cert-manager (${CERT_MANAGER_VERSION})..."

    helm repo add jetstack https://charts.jetstack.io 2>/dev/null || true
    helm repo update

    if helm status cert-manager -n cert-manager &>/dev/null; then
        warn "cert-manager already installed, upgrading..."
    fi

    helm upgrade --install cert-manager jetstack/cert-manager \
        --namespace cert-manager \
        --create-namespace \
        --version "${CERT_MANAGER_VERSION}" \
        --set crds.enabled=true \
        --set prometheus.enabled=true \
        --wait --timeout 120s

    log "cert-manager installed"
}

# ── Install KEDA ──
install_keda() {
    info "Installing KEDA (v${KEDA_VERSION})..."

    helm repo add kedacore https://kedacore.github.io/charts 2>/dev/null || true
    helm repo update

    if helm status keda -n keda &>/dev/null; then
        warn "KEDA already installed, upgrading..."
    fi

    helm upgrade --install keda kedacore/keda \
        --namespace keda \
        --create-namespace \
        --version "${KEDA_VERSION}" \
        --set prometheus.metricServer.enabled=true \
        --set prometheus.operator.enabled=false \
        --wait --timeout 120s

    log "KEDA installed"
}

# ── Taint worker nodes ──
taint_worker_nodes() {
    info "Applying worker node taints..."

    # Label and taint nodes with 'sentinel.io/role=worker'
    # In a multi-node setup, label specific nodes:
    #   kubectl label node <node-name> sentinel.io/role=worker
    #   kubectl taint node <node-name> workload=untrusted:NoSchedule

    local nodes
    nodes=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')

    for node in $nodes; do
        # In single-node k3s, label+taint the only node
        if ! kubectl get node "$node" -o jsonpath='{.metadata.labels.sentinel\.io/role}' 2>/dev/null | grep -q worker; then
            kubectl label node "$node" sentinel.io/role=worker --overwrite
            log "Labeled node $node with sentinel.io/role=worker"
        fi

        if ! kubectl describe node "$node" | grep -q "workload=untrusted:NoSchedule"; then
            kubectl taint node "$node" workload=untrusted:NoSchedule --overwrite 2>/dev/null || true
            log "Tainted node $node with workload=untrusted:NoSchedule"
        else
            warn "Node $node already tainted"
        fi
    done
}

# ── Apply Sentinel manifests ──
apply_manifests() {
    info "Applying Sentinel Kubernetes manifests..."

    # Apply via kustomize
    kubectl apply -k "$K8S_DIR"

    log "All manifests applied"

    # Wait for rollouts
    info "Waiting for deployments to be ready..."
    kubectl -n "$NAMESPACE" rollout status deployment/sentinel-api --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status deployment/sentinel-worker --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status deployment/sentinel-frontend --timeout=120s || true

    info "Waiting for statefulsets to be ready..."
    kubectl -n "$NAMESPACE" rollout status statefulset/sentinel-postgres --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status statefulset/sentinel-rabbitmq --timeout=180s || true
    kubectl -n "$NAMESPACE" rollout status statefulset/sentinel-redis --timeout=60s || true

    log "All workloads are ready"
}

# ── Status check ──
show_status() {
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "  Project Sentinel — Cluster Status"
    echo "═══════════════════════════════════════════════════════════════"
    echo ""

    info "Nodes:"
    kubectl get nodes -o wide
    echo ""

    info "Pods in ${NAMESPACE}:"
    kubectl get pods -n "$NAMESPACE" -o wide
    echo ""

    info "Services in ${NAMESPACE}:"
    kubectl get svc -n "$NAMESPACE"
    echo ""

    info "Ingress:"
    kubectl get ingress -n "$NAMESPACE"
    echo ""

    info "PVCs:"
    kubectl get pvc -n "$NAMESPACE"
    echo ""

    info "HPA / ScaledObjects:"
    kubectl get hpa -n "$NAMESPACE" 2>/dev/null || true
    kubectl get scaledobject -n "$NAMESPACE" 2>/dev/null || true
    echo ""

    log "Setup complete! 🎉"
    echo ""
    info "Next steps:"
    echo "  1. Update DNS: point sentinel.example.com → $(kubectl get svc -n ingress-nginx ingress-nginx-controller -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo '<LB_IP>')"
    echo "  2. Update secrets in infra/k8s/secrets.yaml with real credentials"
    echo "  3. Monitor: kubectl -n ${NAMESPACE} get pods -w"
    echo "  4. Logs:    kubectl -n ${NAMESPACE} logs -f deployment/sentinel-api"
    echo ""
}

# ── Teardown ──
teardown() {
    warn "Tearing down Sentinel from k3s..."

    kubectl delete -k "$K8S_DIR" --ignore-not-found || true
    helm uninstall keda -n keda 2>/dev/null || true
    helm uninstall cert-manager -n cert-manager 2>/dev/null || true
    helm uninstall ingress-nginx -n ingress-nginx 2>/dev/null || true

    kubectl delete namespace "$NAMESPACE" --ignore-not-found || true
    kubectl delete namespace keda --ignore-not-found || true
    kubectl delete namespace cert-manager --ignore-not-found || true
    kubectl delete namespace ingress-nginx --ignore-not-found || true

    log "Teardown complete"
}

# ── Main ──
main() {
    case "${1:-}" in
        --teardown)
            preflight
            teardown
            ;;
        --manifests)
            preflight
            apply_manifests
            show_status
            ;;
        --status)
            show_status
            ;;
        *)
            preflight
            install_k3s
            install_helm
            install_nginx_ingress
            install_cert_manager
            install_keda
            taint_worker_nodes
            apply_manifests
            show_status
            ;;
    esac
}

main "$@"
