#!/bin/sh
# =============================================================================
# wait-for-service.sh — Wait until an HTTP endpoint returns 200
# Usage: ./wait-for-service.sh <url> [timeout_seconds] [interval_seconds]
# Example: ./wait-for-service.sh http://localhost:8080/api/v1/health 60 2
# =============================================================================

set -e

URL="${1:?Usage: $0 <url> [timeout] [interval]}"
TIMEOUT="${2:-60}"
INTERVAL="${3:-2}"
ELAPSED=0

echo "⏳ Waiting for ${URL} to become healthy (timeout: ${TIMEOUT}s)..."

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
    if curl -sf --max-time 5 "$URL" > /dev/null 2>&1; then
        echo "✅ ${URL} is healthy (took ${ELAPSED}s)"
        exit 0
    fi
    sleep "$INTERVAL"
    ELAPSED=$((ELAPSED + INTERVAL))
done

echo "❌ Timed out waiting for ${URL} after ${TIMEOUT}s"
exit 1
