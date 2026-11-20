#!/bin/bash
set -e

###############################################################################
# test-integration.sh - Run integration tests against local stack
#
# Starts docker-compose services, waits for health, runs Python integration
# tests, and cleans up.
#
# Usage:
#   ./scripts/test-integration.sh [--no-cleanup] [--verbose]
#
# Options:
#   --no-cleanup  Keep containers running after tests
#   --verbose     Show verbose test output
###############################################################################

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
COMPOSE_FILE="$PROJECT_ROOT/docker/docker-compose.yml"
CLEANUP=true
VERBOSE=""
EXIT_CODE=0

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --no-cleanup)
            CLEANUP=false
            shift
            ;;
        --verbose|-v)
            VERBOSE="-v"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--no-cleanup] [--verbose]"
            echo ""
            echo "Runs integration tests against the local development stack."
            echo ""
            echo "Options:"
            echo "  --no-cleanup  Keep containers running after tests"
            echo "  --verbose     Show verbose test output"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Cleanup function
cleanup() {
    if [ "$CLEANUP" = true ]; then
        echo ""
        echo "=== Cleaning up ==="
        docker compose -f "$COMPOSE_FILE" down --timeout 10
        echo "Containers stopped."
    else
        echo ""
        echo "Containers left running (--no-cleanup)."
        echo "Stop manually: docker compose -f $COMPOSE_FILE down"
    fi
}

trap cleanup EXIT

# Verify Docker is running
if ! docker info &> /dev/null; then
    echo "Error: Docker is not running"
    exit 1
fi

# Start services
echo "=== Starting local services ==="
docker compose -f "$COMPOSE_FILE" up -d

# Wait for services to be healthy
echo ""
echo "=== Waiting for services to be healthy ==="

wait_for_service() {
    local name=$1
    local container=$2
    local max_wait=${3:-60}
    local elapsed=0

    printf "  %-20s " "$name..."
    while [ $elapsed -lt $max_wait ]; do
        status=$(docker inspect --format='{{.State.Health.Status}}' "$container" 2>/dev/null || echo "not_found")
        if [ "$status" = "healthy" ]; then
            echo "OK (${elapsed}s)"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    echo "TIMEOUT after ${max_wait}s"
    return 1
}

ALL_HEALTHY=true
wait_for_service "MinIO" "opencloud-minio" 30 || ALL_HEALTHY=false
wait_for_service "NATS" "opencloud-nats" 30 || ALL_HEALTHY=false
wait_for_service "Redis" "opencloud-redis" 30 || ALL_HEALTHY=false
wait_for_service "ScyllaDB" "opencloud-scylladb" 60 || ALL_HEALTHY=false

if [ "$ALL_HEALTHY" = false ]; then
    echo ""
    echo "Warning: Not all services are healthy. Tests may fail."
    echo ""
    docker compose -f "$COMPOSE_FILE" ps
fi

# Set environment variables for tests
export CLOUD_ADAPTER_SO_PATH="$PROJECT_ROOT/go/build/libcloudadapter.so"
export OPENCLOUD_STORAGE_URL="s3://test-bucket?endpoint=http://localhost:19000&s3ForcePathStyle=true&disableSSL=true&region=us-east-1"
export OPENCLOUD_PUBSUB_TOPIC_URL="nats://events"
export OPENCLOUD_DATABASE_URL="dynamodb://test-table?endpoint=http://localhost:18000&region=us-east-1"
export OPENCLOUD_SECRETS_URL="redis://localhost:16379"
export AWS_ACCESS_KEY_ID="minioadmin"
export AWS_SECRET_ACCESS_KEY="minioadmin"

# Run Python integration tests
echo ""
echo "=== Running integration tests ==="

if command -v pytest &> /dev/null; then
    pytest tests/test_integration.py $VERBOSE || EXIT_CODE=$?
elif command -v python &> /dev/null; then
    python -m pytest tests/test_integration.py $VERBOSE || EXIT_CODE=$?
else
    echo "Error: pytest or python not found"
    EXIT_CODE=1
fi

if [ $EXIT_CODE -eq 0 ]; then
    echo ""
    echo "=== All integration tests passed ==="
else
    echo ""
    echo "=== Integration tests failed (exit code: $EXIT_CODE) ==="
fi

exit $EXIT_CODE
