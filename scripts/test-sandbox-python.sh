#!/usr/bin/env bash
# =============================================================================
# Sentinel — Python Sandbox Test Suite
# Tests nsjail sandbox isolation for Python code execution.
#
# Prerequisites:
#   - nsjail installed and accessible at NSJAIL_PATH (default: /usr/bin/nsjail)
#   - Python3 installed on the host
#   - Run as root (nsjail needs namespace privileges)
#
# Usage:
#   sudo ./scripts/test-sandbox-python.sh
# =============================================================================
set -euo pipefail

# --- Configuration ---
NSJAIL="${NSJAIL_PATH:-/usr/bin/nsjail}"
CONFIG_DIR="${CONFIG_DIR:-$(dirname "$0")/../sandbox/nsjail}"
POLICY_DIR="${POLICY_DIR:-$(dirname "$0")/../sandbox/policies}"
CONFIG="$CONFIG_DIR/python.cfg"
WORKDIR=""
PASSED=0
FAILED=0
TOTAL=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# --- Helpers ---
setup_workdir() {
    WORKDIR=$(mktemp -d /tmp/sentinel-test-XXXXXX)
}

cleanup_workdir() {
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
}

run_sandbox() {
    local code_file="$1"
    shift
    timeout 30 "$NSJAIL" \
        --config "$CONFIG" \
        --bindmount "$WORKDIR:/tmp/work" \
        -- /usr/bin/python3 /tmp/work/"$code_file" "$@" 2>/tmp/sentinel-nsjail-stderr.log
}

assert_pass() {
    local test_name="$1"
    local exit_code="$2"
    TOTAL=$((TOTAL + 1))
    if [[ "$exit_code" -eq 0 ]]; then
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: $test_name"
    else
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: $test_name (exit code: $exit_code)"
        echo -e "  ${YELLOW}  nsjail stderr:${NC}"
        cat /tmp/sentinel-nsjail-stderr.log 2>/dev/null | head -5 | sed 's/^/    /'
    fi
}

assert_fail() {
    local test_name="$1"
    local exit_code="$2"
    TOTAL=$((TOTAL + 1))
    if [[ "$exit_code" -ne 0 ]]; then
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: $test_name (correctly killed, exit: $exit_code)"
    else
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: $test_name (should have been killed, but exited 0)"
    fi
}

assert_output_contains() {
    local test_name="$1"
    local expected="$2"
    local actual="$3"
    TOTAL=$((TOTAL + 1))
    if echo "$actual" | grep -qF "$expected"; then
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: $test_name"
    else
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: $test_name (expected '$expected' in output)"
        echo -e "  ${YELLOW}  actual:${NC} $(echo "$actual" | head -3)"
    fi
}

# --- Pre-flight checks ---
echo -e "\n${CYAN}=== Sentinel Python Sandbox Tests ===${NC}\n"

if [[ ! -x "$NSJAIL" ]]; then
    echo -e "${RED}ERROR: nsjail not found at $NSJAIL${NC}"
    echo "Install nsjail or set NSJAIL_PATH environment variable."
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    echo -e "${YELLOW}WARNING: Not running as root. Some tests may fail.${NC}"
fi

echo -e "nsjail:  $NSJAIL"
echo -e "config:  $CONFIG"
echo -e "policy:  $POLICY_DIR/python.policy"
echo ""

# =============================================================================
# Test 1: Hello World
# =============================================================================
echo -e "${CYAN}[Test 1] Hello World — basic execution${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
print("Hello, Sentinel!")
PYEOF

output=$(run_sandbox "code.py" 2>/dev/null || true)
assert_output_contains "stdout contains greeting" "Hello, Sentinel!" "$output"
cleanup_workdir

# =============================================================================
# Test 2: stdin handling
# =============================================================================
echo -e "${CYAN}[Test 2] stdin — reading input${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
name = input()
print(f"Hello, {name}!")
PYEOF

output=$(echo "World" | timeout 30 "$NSJAIL" \
    --config "$CONFIG" \
    --bindmount "$WORKDIR:/tmp/work" \
    -- /usr/bin/python3 /tmp/work/code.py 2>/dev/null || true)
assert_output_contains "stdin processed correctly" "Hello, World!" "$output"
cleanup_workdir

# =============================================================================
# Test 3: Fork bomb (pids.max should prevent)
# =============================================================================
echo -e "${CYAN}[Test 3] Fork bomb — cgroup pids limit${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
import os
while True:
    os.fork()
PYEOF

set +e
run_sandbox "code.py" >/dev/null 2>&1
exit_code=$?
set -e
assert_fail "fork bomb killed by pids limit" "$exit_code"
cleanup_workdir

# =============================================================================
# Test 4: Infinite loop (time limit should kill)
# =============================================================================
echo -e "${CYAN}[Test 4] Infinite loop — time limit${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
while True:
    pass
PYEOF

set +e
run_sandbox "code.py" >/dev/null 2>&1
exit_code=$?
set -e
assert_fail "infinite loop killed by time limit" "$exit_code"
cleanup_workdir

# =============================================================================
# Test 5: Memory bomb (cgroup mem limit should OOM-kill)
# =============================================================================
echo -e "${CYAN}[Test 5] Memory bomb — cgroup memory limit${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
data = []
while True:
    data.append("A" * (1024 * 1024))  # 1MB chunks
PYEOF

set +e
run_sandbox "code.py" >/dev/null 2>&1
exit_code=$?
set -e
assert_fail "memory bomb killed by OOM" "$exit_code"
cleanup_workdir

# =============================================================================
# Test 6: Network access (should be blocked)
# =============================================================================
echo -e "${CYAN}[Test 6] Network access — should be blocked${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
import socket
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(("8.8.8.8", 53))
    print("NETWORK_ACCESS_SUCCEEDED")
except Exception as e:
    print(f"NETWORK_BLOCKED: {e}")
PYEOF

set +e
output=$(run_sandbox "code.py" 2>/dev/null)
exit_code=$?
set -e

# Either seccomp kills it (non-zero exit) or the socket call raises an error
if [[ "$exit_code" -ne 0 ]]; then
    TOTAL=$((TOTAL + 1))
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: network blocked by seccomp (killed, exit: $exit_code)"
elif echo "$output" | grep -q "NETWORK_BLOCKED"; then
    TOTAL=$((TOTAL + 1))
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: network blocked by namespace (connection refused)"
elif echo "$output" | grep -q "NETWORK_ACCESS_SUCCEEDED"; then
    TOTAL=$((TOTAL + 1))
    FAILED=$((FAILED + 1))
    echo -e "  ${RED}✗ FAIL${NC}: network access was NOT blocked!"
else
    TOTAL=$((TOTAL + 1))
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: network blocked (exit: $exit_code)"
fi
cleanup_workdir

# =============================================================================
# Test 7: File system isolation (cannot read host files)
# =============================================================================
echo -e "${CYAN}[Test 7] Filesystem isolation — cannot read /etc/passwd${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
try:
    with open("/etc/passwd") as f:
        print("FILE_READ_SUCCEEDED")
        print(f.read()[:100])
except Exception as e:
    print(f"ISOLATED: {e}")
PYEOF

set +e
output=$(run_sandbox "code.py" 2>/dev/null)
exit_code=$?
set -e

if echo "$output" | grep -q "FILE_READ_SUCCEEDED"; then
    TOTAL=$((TOTAL + 1))
    FAILED=$((FAILED + 1))
    echo -e "  ${RED}✗ FAIL${NC}: was able to read /etc/passwd!"
else
    TOTAL=$((TOTAL + 1))
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: filesystem isolated"
fi
cleanup_workdir

# =============================================================================
# Test 8: Exit code propagation
# =============================================================================
echo -e "${CYAN}[Test 8] Exit codes — non-zero exit${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
import sys
sys.exit(42)
PYEOF

set +e
run_sandbox "code.py" >/dev/null 2>&1
exit_code=$?
set -e

TOTAL=$((TOTAL + 1))
if [[ "$exit_code" -eq 42 ]]; then
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: exit code 42 propagated correctly"
else
    # nsjail may remap exit codes; just check it's non-zero
    if [[ "$exit_code" -ne 0 ]]; then
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: non-zero exit code propagated (got $exit_code)"
    else
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: expected non-zero exit code, got 0"
    fi
fi
cleanup_workdir

# =============================================================================
# Test 9: Large output
# =============================================================================
echo -e "${CYAN}[Test 9] Large output — stdout handling${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
for i in range(10000):
    print(f"Line {i}: " + "x" * 100)
PYEOF

set +e
output=$(run_sandbox "code.py" 2>/dev/null)
exit_code=$?
set -e
line_count=$(echo "$output" | wc -l)
TOTAL=$((TOTAL + 1))
if [[ "$line_count" -gt 100 ]]; then
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: large output captured ($line_count lines)"
else
    FAILED=$((FAILED + 1))
    echo -e "  ${RED}✗ FAIL${NC}: output too small ($line_count lines)"
fi
cleanup_workdir

# =============================================================================
# Test 10: Runtime exception (stderr capture)
# =============================================================================
echo -e "${CYAN}[Test 10] Runtime exception — stderr capture${NC}"
setup_workdir
cat > "$WORKDIR/code.py" << 'PYEOF'
raise ValueError("test error message")
PYEOF

set +e
output=$(run_sandbox "code.py" 2>/tmp/sentinel-nsjail-stderr.log)
exit_code=$?
set -e
assert_fail "runtime exception exits non-zero" "$exit_code"
cleanup_workdir

# =============================================================================
# Summary
# =============================================================================
echo ""
echo -e "${CYAN}=== Results ===${NC}"
echo -e "  Total:  $TOTAL"
echo -e "  ${GREEN}Passed: $PASSED${NC}"
if [[ "$FAILED" -gt 0 ]]; then
    echo -e "  ${RED}Failed: $FAILED${NC}"
    echo ""
    exit 1
else
    echo -e "  Failed: 0"
    echo ""
    echo -e "${GREEN}All tests passed! ✓${NC}"
    exit 0
fi
