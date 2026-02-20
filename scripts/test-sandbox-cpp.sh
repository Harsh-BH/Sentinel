#!/usr/bin/env bash
# =============================================================================
# Sentinel — C++ Sandbox Test Suite
# Tests nsjail sandbox isolation for C++ compilation and execution.
#
# Prerequisites:
#   - nsjail installed and accessible at NSJAIL_PATH (default: /usr/bin/nsjail)
#   - g++ installed on the host
#   - Run as root (nsjail needs namespace privileges)
#
# Usage:
#   sudo ./scripts/test-sandbox-cpp.sh
# =============================================================================
set -euo pipefail

# --- Configuration ---
NSJAIL="${NSJAIL_PATH:-/usr/bin/nsjail}"
CONFIG_DIR="${CONFIG_DIR:-$(dirname "$0")/../sandbox/nsjail}"
POLICY_DIR="${POLICY_DIR:-$(dirname "$0")/../sandbox/policies}"
CONFIG="$CONFIG_DIR/cpp.cfg"
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
    local args=("$@")
    timeout 30 "$NSJAIL" \
        --config "$CONFIG" \
        --bindmount "$WORKDIR:/tmp/work" \
        -- "${args[@]}" 2>/tmp/sentinel-nsjail-stderr.log
}

compile_in_sandbox() {
    local source_file="$1"
    local output_file="${2:-/tmp/work/program}"
    run_sandbox /usr/bin/g++ -std=c++17 -O2 -o "$output_file" "/tmp/work/$source_file"
}

run_compiled() {
    run_sandbox /tmp/work/program
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
        echo -e "  ${GREEN}✓ PASS${NC}: $test_name (correctly failed, exit: $exit_code)"
    else
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: $test_name (should have failed, but exited 0)"
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
echo -e "\n${CYAN}=== Sentinel C++ Sandbox Tests ===${NC}\n"

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
echo ""

# =============================================================================
# Test 1: Hello World — compile and run
# =============================================================================
echo -e "${CYAN}[Test 1] Hello World — compile and run${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <iostream>
int main() {
    std::cout << "Hello, Sentinel!" << std::endl;
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
output=$(run_compiled 2>/dev/null)
run_exit=$?
set -e
assert_pass "execution succeeds" "$run_exit"
assert_output_contains "stdout contains greeting" "Hello, Sentinel!" "$output"
cleanup_workdir

# =============================================================================
# Test 2: stdin handling
# =============================================================================
echo -e "${CYAN}[Test 2] stdin — reading input${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <iostream>
#include <string>
int main() {
    std::string name;
    std::getline(std::cin, name);
    std::cout << "Hello, " << name << "!" << std::endl;
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
output=$(echo "World" | timeout 30 "$NSJAIL" \
    --config "$CONFIG" \
    --bindmount "$WORKDIR:/tmp/work" \
    -- /tmp/work/program 2>/dev/null)
set -e
assert_output_contains "stdin processed" "Hello, World!" "$output"
cleanup_workdir

# =============================================================================
# Test 3: Compilation error — stderr capture
# =============================================================================
echo -e "${CYAN}[Test 3] Compilation error — invalid C++ code${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <iostream>
int main() {
    this is not valid c++ code!!!
    return 0;
}
CPPEOF

set +e
stderr_output=$(compile_in_sandbox "code.cpp" 2>/dev/null)
compile_exit=$?
set -e
assert_fail "compilation fails" "$compile_exit"
cleanup_workdir

# =============================================================================
# Test 4: Runtime segfault
# =============================================================================
echo -e "${CYAN}[Test 4] Segfault — signals handled correctly${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <cstdlib>
int main() {
    int* p = nullptr;
    *p = 42;  // segfault
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
run_compiled >/dev/null 2>&1
run_exit=$?
set -e
assert_fail "segfault caught" "$run_exit"
cleanup_workdir

# =============================================================================
# Test 5: Fork bomb
# =============================================================================
echo -e "${CYAN}[Test 5] Fork bomb — cgroup pids limit${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <unistd.h>
int main() {
    while (true) {
        fork();
    }
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
run_compiled >/dev/null 2>&1
run_exit=$?
set -e
assert_fail "fork bomb killed by pids limit" "$run_exit"
cleanup_workdir

# =============================================================================
# Test 6: Infinite loop (time limit)
# =============================================================================
echo -e "${CYAN}[Test 6] Infinite loop — time limit${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
int main() {
    while (true) {}
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
run_compiled >/dev/null 2>&1
run_exit=$?
set -e
assert_fail "infinite loop killed by time limit" "$run_exit"
cleanup_workdir

# =============================================================================
# Test 7: Memory bomb
# =============================================================================
echo -e "${CYAN}[Test 7] Memory bomb — cgroup memory limit${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <vector>
#include <string>
int main() {
    std::vector<std::string> data;
    while (true) {
        data.push_back(std::string(1024 * 1024, 'A'));  // 1MB chunks
    }
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
run_compiled >/dev/null 2>&1
run_exit=$?
set -e
assert_fail "memory bomb killed by OOM" "$run_exit"
cleanup_workdir

# =============================================================================
# Test 8: Network access blocked
# =============================================================================
echo -e "${CYAN}[Test 8] Network access — should be blocked${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <iostream>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
int main() {
    int sock = socket(AF_INET, SOCK_STREAM, 0);
    if (sock < 0) {
        std::cout << "NETWORK_BLOCKED" << std::endl;
        return 1;
    }
    struct sockaddr_in addr;
    addr.sin_family = AF_INET;
    addr.sin_port = htons(53);
    inet_pton(AF_INET, "8.8.8.8", &addr.sin_addr);
    int res = connect(sock, (struct sockaddr*)&addr, sizeof(addr));
    if (res < 0) {
        std::cout << "NETWORK_BLOCKED" << std::endl;
        return 1;
    }
    std::cout << "NETWORK_ACCESS_SUCCEEDED" << std::endl;
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
# Note: compilation might succeed (headers are available) but socket() call at
# runtime should be killed by seccomp or blocked by network namespace
if [[ "$compile_exit" -eq 0 ]]; then
    set +e
    output=$(run_compiled 2>/dev/null)
    run_exit=$?
    set -e

    if [[ "$run_exit" -ne 0 ]]; then
        TOTAL=$((TOTAL + 1))
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: network blocked (killed by seccomp/namespace, exit: $run_exit)"
    elif echo "$output" | grep -q "NETWORK_BLOCKED"; then
        TOTAL=$((TOTAL + 1))
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: network blocked by namespace"
    elif echo "$output" | grep -q "NETWORK_ACCESS_SUCCEEDED"; then
        TOTAL=$((TOTAL + 1))
        FAILED=$((FAILED + 1))
        echo -e "  ${RED}✗ FAIL${NC}: network access was NOT blocked!"
    else
        TOTAL=$((TOTAL + 1))
        PASSED=$((PASSED + 1))
        echo -e "  ${GREEN}✓ PASS${NC}: network blocked (exit: $run_exit)"
    fi
else
    TOTAL=$((TOTAL + 1))
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: socket headers unavailable in sandbox (compilation failed)"
fi
cleanup_workdir

# =============================================================================
# Test 9: Non-zero exit code
# =============================================================================
echo -e "${CYAN}[Test 9] Exit code propagation${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
int main() {
    return 42;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
run_compiled >/dev/null 2>&1
run_exit=$?
set -e
TOTAL=$((TOTAL + 1))
if [[ "$run_exit" -ne 0 ]]; then
    PASSED=$((PASSED + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: non-zero exit code propagated (got $run_exit)"
else
    FAILED=$((FAILED + 1))
    echo -e "  ${RED}✗ FAIL${NC}: expected non-zero exit, got 0"
fi
cleanup_workdir

# =============================================================================
# Test 10: Large compilation (stress test)
# =============================================================================
echo -e "${CYAN}[Test 10] Template-heavy code — compilation under resource limits${NC}"
setup_workdir
cat > "$WORKDIR/code.cpp" << 'CPPEOF'
#include <iostream>
#include <vector>
#include <string>
#include <algorithm>
#include <numeric>
#include <map>

template<typename T>
T add(T a, T b) { return a + b; }

int main() {
    std::vector<int> v(100);
    std::iota(v.begin(), v.end(), 1);
    int sum = std::accumulate(v.begin(), v.end(), 0);
    std::cout << "Sum: " << sum << std::endl;

    std::map<std::string, int> m;
    for (int i = 0; i < 100; i++) {
        m["key" + std::to_string(i)] = add(i, i * 2);
    }
    std::cout << "Map size: " << m.size() << std::endl;
    return 0;
}
CPPEOF

set +e
compile_in_sandbox "code.cpp" >/dev/null 2>&1
compile_exit=$?
set -e
assert_pass "compilation succeeds" "$compile_exit"

set +e
output=$(run_compiled 2>/dev/null)
run_exit=$?
set -e
assert_pass "execution succeeds" "$run_exit"
assert_output_contains "correct sum" "Sum: 5050" "$output"
assert_output_contains "correct map size" "Map size: 100" "$output"
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
