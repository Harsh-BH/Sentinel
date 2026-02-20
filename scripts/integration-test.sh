#!/bin/sh
# =============================================================================
# Project Sentinel — Integration Test Script
# =============================================================================
# Runs inside docker-compose.test.yml as the 'integration-test' service.
# Validates end-to-end: submit code → poll until terminal → check results.
#
# Exit 0 = all tests pass, Exit 1 = failure
# =============================================================================

set -e

API_BASE="${API_BASE:-http://api:8080}"
POLL_TIMEOUT=60
POLL_INTERVAL=2
PASSED=0
FAILED=0
TOTAL=0

# ── Helpers ──────────────────────────────────────────────────

log()   { echo "  [INFO]  $*"; }
pass()  { PASSED=$((PASSED + 1)); echo "  ✅ PASS: $*"; }
fail()  { FAILED=$((FAILED + 1)); echo "  ❌ FAIL: $*"; }

# Wait for API health check
wait_for_api() {
    echo "⏳ Waiting for API to become healthy..."
    elapsed=0
    while [ "$elapsed" -lt 120 ]; do
        if curl -sf --max-time 5 "${API_BASE}/api/v1/health" > /dev/null 2>&1; then
            echo "✅ API is healthy (${elapsed}s)"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    echo "❌ API did not become healthy within 120s"
    exit 1
}

# Submit code and return job_id
# Usage: submit_code <language> <source_code> [stdin]
submit_code() {
    _lang="$1"
    _code="$2"
    _stdin="${3:-}"

    _payload=$(cat <<ENDJSON
{
    "language": "${_lang}",
    "source_code": $(printf '%s' "$_code" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\t/\\t/g; s/$/\\n/' | tr -d '\n' | sed 's/^/"/; s/\\n$/\\n"/'),
    "stdin": "${_stdin}"
}
ENDJSON
)

    _response=$(curl -sf --max-time 10 \
        -X POST \
        -H "Content-Type: application/json" \
        -d "$_payload" \
        "${API_BASE}/api/v1/submissions" 2>&1) || {
        echo ""
        return 1
    }

    echo "$_response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p'
}

# Poll for terminal status, return full response JSON
# Usage: poll_job <job_id>
poll_job() {
    _job_id="$1"
    _elapsed=0

    while [ "$_elapsed" -lt "$POLL_TIMEOUT" ]; do
        _resp=$(curl -sf --max-time 5 "${API_BASE}/api/v1/submissions/${_job_id}" 2>&1) || {
            sleep "$POLL_INTERVAL"
            _elapsed=$((_elapsed + POLL_INTERVAL))
            continue
        }

        _status=$(echo "$_resp" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')

        case "$_status" in
            SUCCESS|COMPILATION_ERROR|RUNTIME_ERROR|TIMEOUT|MEMORY_LIMIT_EXCEEDED|INTERNAL_ERROR)
                echo "$_resp"
                return 0
                ;;
        esac

        sleep "$POLL_INTERVAL"
        _elapsed=$((_elapsed + POLL_INTERVAL))
    done

    echo ""
    return 1
}

# Extract a JSON field value (simple grep-based, no jq dependency)
json_field() {
    echo "$1" | sed -n "s/.*\"$2\":\"\{0,1\}\([^,\"}\]*\)\"\{0,1\}.*/\1/p" | head -1
}

# ── Test Cases ───────────────────────────────────────────────

run_test() {
    _test_name="$1"
    _lang="$2"
    _code="$3"
    _stdin="$4"
    _expected_status="$5"
    _expected_stdout="$6"

    TOTAL=$((TOTAL + 1))
    log "Test ${TOTAL}: ${_test_name}"

    # Submit
    _job_id=$(submit_code "$_lang" "$_code" "$_stdin")
    if [ -z "$_job_id" ]; then
        fail "${_test_name} — submission failed"
        return
    fi
    log "  Job ID: ${_job_id}"

    # Poll
    _result=$(poll_job "$_job_id")
    if [ -z "$_result" ]; then
        fail "${_test_name} — timed out waiting for result"
        return
    fi

    _actual_status=$(json_field "$_result" "status")
    log "  Status: ${_actual_status}"

    # Check status
    if [ "$_actual_status" != "$_expected_status" ]; then
        fail "${_test_name} — expected status '${_expected_status}', got '${_actual_status}'"
        log "  Response: ${_result}"
        return
    fi

    # Check stdout (if expected)
    if [ -n "$_expected_stdout" ]; then
        _actual_stdout=$(json_field "$_result" "stdout")
        # Trim trailing newlines for comparison
        _trimmed=$(echo "$_actual_stdout" | sed 's/\\n$//')
        _expected_trimmed=$(echo "$_expected_stdout" | sed 's/\\n$//')
        if [ "$_trimmed" != "$_expected_trimmed" ]; then
            fail "${_test_name} — expected stdout '${_expected_stdout}', got '${_actual_stdout}'"
            return
        fi
    fi

    pass "${_test_name}"
}

# ── Main ─────────────────────────────────────────────────────

echo ""
echo "============================================="
echo "  Project Sentinel — Integration Tests"
echo "============================================="
echo ""

wait_for_api

echo ""
echo "── Running Tests ──"
echo ""

# Test 1: Python Hello World
run_test \
    "Python Hello World" \
    "python" \
    "print('hello sentinel')" \
    "" \
    "SUCCESS" \
    "hello sentinel"

# Test 2: C++ Hello World
run_test \
    "C++ Hello World" \
    "cpp" \
    '#include <iostream>\nint main() { std::cout << "hello cpp" << std::endl; return 0; }' \
    "" \
    "SUCCESS" \
    "hello cpp"

# Test 3: Python with stdin
run_test \
    "Python with stdin" \
    "python" \
    "import sys; print(sys.stdin.read().strip().upper())" \
    "hello world" \
    "SUCCESS" \
    "HELLO WORLD"

# Test 4: C++ Compilation Error
run_test \
    "C++ Compilation Error" \
    "cpp" \
    "int main() { this is not valid c++; }" \
    "" \
    "COMPILATION_ERROR" \
    ""

# Test 5: Python Runtime Error
run_test \
    "Python Runtime Error" \
    "python" \
    "raise Exception('test error')" \
    "" \
    "RUNTIME_ERROR" \
    ""

# Test 6: Python Timeout (infinite loop)
run_test \
    "Python Timeout" \
    "python" \
    "while True: pass" \
    "" \
    "TIMEOUT" \
    ""

# ── Summary ──────────────────────────────────────────────────

echo ""
echo "============================================="
echo "  Results: ${PASSED}/${TOTAL} passed, ${FAILED} failed"
echo "============================================="
echo ""

if [ "$FAILED" -gt 0 ]; then
    exit 1
fi

exit 0
