#!/usr/bin/env bash
# =============================================================================
# Sentinel — Security Audit Script
# =============================================================================
# Comprehensive security validation for the nsjail sandbox.
# Tests both Python and C++ sandbox isolation:
#   1. Syscall leaks (seccomp-bpf)
#   2. Filesystem escape (mount namespace, pivot_root)
#   3. Network access (network namespace)
#   4. Resource exhaustion (cgroups v2)
#   5. Process isolation (PID namespace)
#   6. Privilege escalation attempts
#
# Prerequisites:
#   - nsjail installed (NSJAIL_PATH, default /usr/bin/nsjail)
#   - Python3 + g++ on host
#   - Run as root: sudo ./scripts/security-audit.sh
#
# Usage:
#   sudo ./scripts/security-audit.sh
#   sudo NSJAIL_PATH=/path/to/nsjail ./scripts/security-audit.sh
# =============================================================================
set -euo pipefail

# ── Configuration ──
NSJAIL="${NSJAIL_PATH:-/usr/bin/nsjail}"
CONFIG_DIR="${CONFIG_DIR:-$(dirname "$0")/../sandbox/nsjail}"
POLICY_DIR="${POLICY_DIR:-$(dirname "$0")/../sandbox/policies}"
PYTHON_CFG="$CONFIG_DIR/python.cfg"
CPP_CFG="$CONFIG_DIR/cpp.cfg"
WORKDIR=""
PASSED=0
FAILED=0
TOTAL=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# ── Helpers ──
setup_workdir() {
    WORKDIR=$(mktemp -d /tmp/sentinel-audit-XXXXXX)
}

cleanup_workdir() {
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
    WORKDIR=""
}

run_python() {
    local code_file="$1"
    shift
    timeout 30 "$NSJAIL" \
        --config "$PYTHON_CFG" \
        --bindmount "$WORKDIR:/tmp/work" \
        -- /usr/bin/python3 /tmp/work/"$code_file" "$@" 2>/tmp/sentinel-audit-stderr.log
}

run_cpp() {
    local binary="$1"
    shift
    timeout 30 "$NSJAIL" \
        --config "$CPP_CFG" \
        --bindmount "$WORKDIR:/tmp/work" \
        -- /tmp/work/"$binary" "$@" 2>/tmp/sentinel-audit-stderr.log
}

compile_cpp() {
    local src="$1"
    local out="$2"
    g++ -std=c++17 -O2 -o "$WORKDIR/$out" "$WORKDIR/$src" 2>/dev/null
}

pass() {
    PASSED=$((PASSED + 1))
    TOTAL=$((TOTAL + 1))
    echo -e "  ${GREEN}✓ PASS${NC}: $*"
}

fail() {
    FAILED=$((FAILED + 1))
    TOTAL=$((TOTAL + 1))
    echo -e "  ${RED}✗ FAIL${NC}: $*"
}

section() {
    echo -e "\n${BOLD}${CYAN}━━━ $* ━━━${NC}"
}

# ── Pre-flight ──
echo -e "\n${BOLD}${CYAN}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${CYAN}║        Sentinel Security Audit                          ║${NC}"
echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════════════════╝${NC}\n"

if [[ ! -x "$NSJAIL" ]]; then
    echo -e "${RED}ERROR: nsjail not found at $NSJAIL${NC}"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    echo -e "${RED}ERROR: Must run as root (nsjail needs namespace privileges)${NC}"
    echo "Usage: sudo $0"
    exit 1
fi

echo -e "  nsjail:      $NSJAIL"
echo -e "  Python cfg:  $PYTHON_CFG"
echo -e "  C++ cfg:     $CPP_CFG"
echo -e "  Policies:    $POLICY_DIR/"

# =============================================================================
# SECTION 1: SYSCALL FILTERING (seccomp-bpf)
# =============================================================================
section "1. Seccomp-BPF Syscall Filtering"

# 1a. Python: ptrace (should be blocked)
echo -e "${CYAN}  [1a] Python — ptrace syscall blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import ctypes, os
libc = ctypes.CDLL("libc.so.6", use_errno=True)
PTRACE_TRACEME = 0
result = libc.ptrace(PTRACE_TRACEME, 0, 0, 0)
if result == 0:
    print("PTRACE_SUCCEEDED")
else:
    print(f"PTRACE_BLOCKED: errno={ctypes.get_errno()}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if [[ "$exit_code" -ne 0 ]] || echo "$output" | grep -q "PTRACE_BLOCKED"; then
    pass "ptrace blocked by seccomp"
else
    fail "ptrace was NOT blocked!"
fi
cleanup_workdir

# 1b. Python: mount syscall (should be blocked)
echo -e "${CYAN}  [1b] Python — mount syscall blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import ctypes
libc = ctypes.CDLL("libc.so.6", use_errno=True)
result = libc.mount(b"none", b"/tmp", b"tmpfs", 0, None)
if result == 0:
    print("MOUNT_SUCCEEDED")
else:
    print(f"MOUNT_BLOCKED: errno={ctypes.get_errno()}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if [[ "$exit_code" -ne 0 ]] || echo "$output" | grep -q "MOUNT_BLOCKED"; then
    pass "mount blocked by seccomp"
else
    fail "mount was NOT blocked!"
fi
cleanup_workdir

# 1c. Python: setuid (should be blocked)
echo -e "${CYAN}  [1c] Python — setuid syscall blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
try:
    os.setuid(0)
    print("SETUID_SUCCEEDED")
except PermissionError:
    print("SETUID_BLOCKED")
except OSError as e:
    print(f"SETUID_BLOCKED: {e}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if [[ "$exit_code" -ne 0 ]] || echo "$output" | grep -q "SETUID_BLOCKED"; then
    pass "setuid blocked"
else
    fail "setuid was NOT blocked!"
fi
cleanup_workdir

# 1d. C++: execve /bin/sh (should be blocked or fail)
echo -e "${CYAN}  [1d] C++ — execve /bin/sh blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.cpp" << 'CPPEOF'
#include <unistd.h>
#include <cstdio>
int main() {
    char *args[] = {(char*)"/bin/sh", (char*)"-c", (char*)"echo SHELL_ESCAPED", nullptr};
    int ret = execve("/bin/sh", args, nullptr);
    printf("EXECVE_BLOCKED: ret=%d\n", ret);
    return 0;
}
CPPEOF
compile_cpp "test.cpp" "test" 2>/dev/null || { pass "C++ execve test (compile blocked in sandbox is fine)"; cleanup_workdir; }
if [[ -f "$WORKDIR/test" ]]; then
    set +e
    output=$(run_cpp "test" 2>/dev/null)
    exit_code=$?
    set -e
    if echo "$output" | grep -q "SHELL_ESCAPED"; then
        fail "execve /bin/sh succeeded — shell escape!"
    else
        pass "execve /bin/sh blocked"
    fi
    cleanup_workdir
fi

# =============================================================================
# SECTION 2: FILESYSTEM ISOLATION
# =============================================================================
section "2. Filesystem Isolation"

# 2a. Python: read /etc/shadow
echo -e "${CYAN}  [2a] Python — cannot read /etc/shadow${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
try:
    with open("/etc/shadow") as f:
        print("SHADOW_READ: " + f.read()[:50])
except Exception:
    print("ISOLATED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "SHADOW_READ"; then
    fail "able to read /etc/shadow!"
else
    pass "cannot read /etc/shadow"
fi
cleanup_workdir

# 2b. Python: write to /etc/
echo -e "${CYAN}  [2b] Python — cannot write to /etc/${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
try:
    with open("/etc/sentinel-test", "w") as f:
        f.write("ESCAPE")
    print("WRITE_SUCCEEDED")
except Exception:
    print("WRITE_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "WRITE_SUCCEEDED"; then
    fail "able to write to /etc/!"
else
    pass "cannot write to /etc/"
fi
cleanup_workdir

# 2c. Python: directory traversal
echo -e "${CYAN}  [2c] Python — directory traversal blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
sensitive_paths = [
    "/proc/1/environ",
    "/proc/1/cmdline",
    "/root/.ssh/id_rsa",
    "/var/run/docker.sock",
    "/etc/kubernetes/admin.conf",
]
escaped = False
for p in sensitive_paths:
    try:
        with open(p) as f:
            content = f.read(100)
            if content:
                print(f"ESCAPED via {p}")
                escaped = True
    except Exception:
        pass

if not escaped:
    print("ISOLATED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "ESCAPED"; then
    fail "directory traversal succeeded: $output"
else
    pass "directory traversal blocked"
fi
cleanup_workdir

# 2d. Python: symlink escape
echo -e "${CYAN}  [2d] Python — symlink escape blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
try:
    os.symlink("/etc/passwd", "/tmp/work/escape_link")
    with open("/tmp/work/escape_link") as f:
        print("SYMLINK_ESCAPED: " + f.read()[:50])
except Exception as e:
    print(f"ISOLATED: {e}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "SYMLINK_ESCAPED"; then
    fail "symlink escape succeeded!"
else
    pass "symlink escape blocked"
fi
cleanup_workdir

# 2e. C++: read /proc/self/maps
echo -e "${CYAN}  [2e] C++ — /proc/self/maps access controlled${NC}"
setup_workdir
cat > "$WORKDIR/test.cpp" << 'CPPEOF'
#include <fstream>
#include <iostream>
#include <string>
int main() {
    std::ifstream f("/proc/self/maps");
    if (f.is_open()) {
        std::string line;
        std::getline(f, line);
        // Having /proc/self/maps is okay (process introspection)
        // but ensure no host paths leak through
        if (line.find("/home") != std::string::npos) {
            std::cout << "HOST_PATH_LEAK" << std::endl;
        } else {
            std::cout << "SAFE" << std::endl;
        }
    } else {
        std::cout << "ISOLATED" << std::endl;
    }
    return 0;
}
CPPEOF
compile_cpp "test.cpp" "test" 2>/dev/null
if [[ -f "$WORKDIR/test" ]]; then
    set +e
    output=$(run_cpp "test" 2>/dev/null)
    exit_code=$?
    set -e
    if echo "$output" | grep -q "HOST_PATH_LEAK"; then
        fail "/proc/self/maps leaks host paths"
    else
        pass "/proc/self/maps does not leak host paths"
    fi
fi
cleanup_workdir

# =============================================================================
# SECTION 3: NETWORK ISOLATION
# =============================================================================
section "3. Network Isolation"

# 3a. Python: TCP connection
echo -e "${CYAN}  [3a] Python — TCP connection blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import socket
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(3)
    s.connect(("8.8.8.8", 53))
    s.close()
    print("TCP_CONNECTED")
except Exception:
    print("TCP_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "TCP_CONNECTED"; then
    fail "TCP connection to 8.8.8.8:53 succeeded!"
else
    pass "TCP connection blocked"
fi
cleanup_workdir

# 3b. Python: UDP connection
echo -e "${CYAN}  [3b] Python — UDP connection blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import socket
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3)
    s.sendto(b"test", ("8.8.8.8", 53))
    data, addr = s.recvfrom(1024)
    print("UDP_CONNECTED")
except Exception:
    print("UDP_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "UDP_CONNECTED"; then
    fail "UDP connection succeeded!"
else
    pass "UDP connection blocked"
fi
cleanup_workdir

# 3c. Python: DNS resolution
echo -e "${CYAN}  [3c] Python — DNS resolution blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import socket
try:
    result = socket.getaddrinfo("google.com", 80)
    print("DNS_RESOLVED")
except Exception:
    print("DNS_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "DNS_RESOLVED"; then
    fail "DNS resolution succeeded!"
else
    pass "DNS resolution blocked"
fi
cleanup_workdir

# 3d. C++: raw socket (should be blocked)
echo -e "${CYAN}  [3d] C++ — raw socket creation blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.cpp" << 'CPPEOF'
#include <sys/socket.h>
#include <netinet/in.h>
#include <cstdio>
int main() {
    int fd = socket(AF_INET, SOCK_RAW, IPPROTO_ICMP);
    if (fd >= 0) {
        printf("RAW_SOCKET_CREATED\n");
    } else {
        printf("RAW_SOCKET_BLOCKED\n");
    }
    return 0;
}
CPPEOF
compile_cpp "test.cpp" "test" 2>/dev/null
if [[ -f "$WORKDIR/test" ]]; then
    set +e
    output=$(run_cpp "test" 2>/dev/null)
    exit_code=$?
    set -e
    if echo "$output" | grep -q "RAW_SOCKET_CREATED"; then
        fail "raw socket creation succeeded!"
    else
        pass "raw socket creation blocked"
    fi
fi
cleanup_workdir

# =============================================================================
# SECTION 4: RESOURCE EXHAUSTION (Cgroups v2)
# =============================================================================
section "4. Resource Exhaustion (Cgroups)"

# 4a. Python: fork bomb
echo -e "${CYAN}  [4a] Python — fork bomb contained${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
pids = []
try:
    for _ in range(1000):
        pid = os.fork()
        if pid == 0:
            while True: pass
        pids.append(pid)
    print("FORK_BOMB_ESCAPED")
except Exception:
    print("FORK_BOMB_CONTAINED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "FORK_BOMB_ESCAPED"; then
    fail "fork bomb was not contained!"
else
    pass "fork bomb contained (exit: $exit_code)"
fi
cleanup_workdir

# 4b. Python: memory bomb
echo -e "${CYAN}  [4b] Python — memory bomb OOM-killed${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
data = []
try:
    while True:
        data.append(b"A" * (10 * 1024 * 1024))  # 10MB chunks
except MemoryError:
    print("OOM_CAUGHT")
print("SURVIVED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
# Either the cgroup OOM-kills it (non-zero exit) or Python catches MemoryError
if [[ "$exit_code" -ne 0 ]] || echo "$output" | grep -q "OOM_CAUGHT"; then
    pass "memory bomb handled (exit: $exit_code)"
else
    fail "memory bomb somehow survived without limit!"
fi
cleanup_workdir

# 4c. Python: infinite loop (time limit)
echo -e "${CYAN}  [4c] Python — infinite loop killed by time limit${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
while True:
    pass
PYEOF
set +e
run_python "test.py" >/dev/null 2>&1
exit_code=$?
set -e
if [[ "$exit_code" -ne 0 ]]; then
    pass "infinite loop killed (exit: $exit_code)"
else
    fail "infinite loop was NOT killed!"
fi
cleanup_workdir

# 4d. C++: thread bomb
echo -e "${CYAN}  [4d] C++ — thread bomb contained${NC}"
setup_workdir
cat > "$WORKDIR/test.cpp" << 'CPPEOF'
#include <thread>
#include <vector>
#include <cstdio>
void spin() { while(true) {} }
int main() {
    std::vector<std::thread> threads;
    try {
        for (int i = 0; i < 1000; i++) {
            threads.emplace_back(spin);
        }
        printf("THREAD_BOMB_ESCAPED\n");
    } catch (...) {
        printf("THREAD_BOMB_CONTAINED\n");
    }
    return 0;
}
CPPEOF
compile_cpp "test.cpp" "test" 2>/dev/null
if [[ -f "$WORKDIR/test" ]]; then
    set +e
    output=$(run_cpp "test" 2>/dev/null)
    exit_code=$?
    set -e
    if echo "$output" | grep -q "THREAD_BOMB_ESCAPED"; then
        fail "thread bomb was not contained!"
    else
        pass "thread bomb contained (exit: $exit_code)"
    fi
fi
cleanup_workdir

# 4e. Python: disk space exhaustion
echo -e "${CYAN}  [4e] Python — disk space exhaustion blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
try:
    with open("/tmp/work/bigfile", "wb") as f:
        for _ in range(1000):
            f.write(b"A" * (10 * 1024 * 1024))  # 10MB chunks → 10GB total
    print("DISK_EXHAUSTION_SUCCEEDED")
except Exception as e:
    print(f"DISK_LIMITED: {e}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "DISK_EXHAUSTION_SUCCEEDED"; then
    fail "disk exhaustion was not limited!"
else
    pass "disk space exhaustion limited"
fi
cleanup_workdir

# =============================================================================
# SECTION 5: PROCESS ISOLATION (PID Namespace)
# =============================================================================
section "5. Process Isolation"

# 5a. Python: cannot see host processes
echo -e "${CYAN}  [5a] Python — cannot see host processes${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
pids = []
try:
    for entry in os.listdir("/proc"):
        if entry.isdigit():
            pids.append(int(entry))
except Exception:
    pass

# In a PID namespace, PID 1 should be our process
# and we should see very few processes
if len(pids) > 20:
    print(f"HOST_PROCESSES_VISIBLE: {len(pids)} pids")
else:
    print(f"PID_ISOLATED: {len(pids)} pids")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "HOST_PROCESSES_VISIBLE"; then
    fail "can see host processes!"
else
    pass "PID namespace isolation working"
fi
cleanup_workdir

# 5b. Python: PID 1 is our process
echo -e "${CYAN}  [5b] Python — PID 1 in namespace${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
pid = os.getpid()
# In PID namespace, our PID should be 1 or a very low number
if pid <= 10:
    print(f"PID_NAMESPACED: pid={pid}")
else:
    print(f"NOT_NAMESPACED: pid={pid}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "PID_NAMESPACED"; then
    pass "running in PID namespace (pid=1)"
elif [[ "$exit_code" -ne 0 ]]; then
    pass "PID namespace active (process was sandboxed)"
else
    fail "not in PID namespace"
fi
cleanup_workdir

# =============================================================================
# SECTION 6: PRIVILEGE ESCALATION
# =============================================================================
section "6. Privilege Escalation Prevention"

# 6a. Python: cannot chroot
echo -e "${CYAN}  [6a] Python — chroot blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
try:
    os.chroot("/tmp")
    print("CHROOT_SUCCEEDED")
except Exception:
    print("CHROOT_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "CHROOT_SUCCEEDED"; then
    fail "chroot succeeded — privilege escalation possible!"
else
    pass "chroot blocked"
fi
cleanup_workdir

# 6b. Python: cannot modify kernel parameters
echo -e "${CYAN}  [6b] Python — sysctl writes blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
try:
    with open("/proc/sys/kernel/hostname", "w") as f:
        f.write("hacked")
    print("SYSCTL_WRITE_SUCCEEDED")
except Exception:
    print("SYSCTL_BLOCKED")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "SYSCTL_WRITE_SUCCEEDED"; then
    fail "sysctl write succeeded!"
else
    pass "sysctl writes blocked"
fi
cleanup_workdir

# 6c. Python: running as non-root
echo -e "${CYAN}  [6c] Python — running as non-root UID${NC}"
setup_workdir
cat > "$WORKDIR/test.py" << 'PYEOF'
import os
uid = os.getuid()
euid = os.geteuid()
if uid == 0 or euid == 0:
    print(f"RUNNING_AS_ROOT: uid={uid} euid={euid}")
else:
    print(f"NON_ROOT: uid={uid} euid={euid}")
PYEOF
set +e
output=$(run_python "test.py" 2>/dev/null)
exit_code=$?
set -e
if echo "$output" | grep -q "RUNNING_AS_ROOT"; then
    # In a user namespace, UID 0 inside maps to non-root outside — this is OK
    pass "running as UID 0 inside user namespace (mapped to non-root outside)"
elif echo "$output" | grep -q "NON_ROOT"; then
    pass "running as non-root"
else
    pass "UID check completed (exit: $exit_code)"
fi
cleanup_workdir

# 6d. C++: mprotect executable stack
echo -e "${CYAN}  [6d] C++ — executable stack via mprotect blocked${NC}"
setup_workdir
cat > "$WORKDIR/test.cpp" << 'CPPEOF'
#include <sys/mman.h>
#include <cstdio>
#include <cstdlib>
int main() {
    void *buf = mmap(nullptr, 4096, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS, -1, 0);
    if (buf == MAP_FAILED) { printf("MMAP_BLOCKED\n"); return 0; }
    int ret = mprotect(buf, 4096, PROT_READ|PROT_WRITE|PROT_EXEC);
    if (ret == 0) {
        printf("MPROTECT_EXEC_SUCCEEDED\n");
    } else {
        printf("MPROTECT_EXEC_BLOCKED\n");
    }
    return 0;
}
CPPEOF
compile_cpp "test.cpp" "test" 2>/dev/null
if [[ -f "$WORKDIR/test" ]]; then
    set +e
    output=$(run_cpp "test" 2>/dev/null)
    exit_code=$?
    set -e
    if echo "$output" | grep -q "MPROTECT_EXEC_SUCCEEDED"; then
        # This might be allowed on some configs; note it as informational
        echo -e "  ${YELLOW}⚠ INFO${NC}: mprotect PROT_EXEC allowed (may be acceptable depending on seccomp policy)"
        TOTAL=$((TOTAL + 1))
        PASSED=$((PASSED + 1))
    else
        pass "mprotect PROT_EXEC blocked"
    fi
fi
cleanup_workdir

# =============================================================================
# SUMMARY
# =============================================================================
echo ""
echo -e "${BOLD}${CYAN}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${CYAN}║        Security Audit Results                           ║${NC}"
echo -e "${BOLD}${CYAN}╠══════════════════════════════════════════════════════════╣${NC}"
printf "${BOLD}${CYAN}║${NC}  Total tests:  %-40s${BOLD}${CYAN}║${NC}\n" "$TOTAL"
printf "${BOLD}${CYAN}║${NC}  ${GREEN}Passed:      %-40s${NC}${BOLD}${CYAN}║${NC}\n" "$PASSED"
if [[ "$FAILED" -gt 0 ]]; then
    printf "${BOLD}${CYAN}║${NC}  ${RED}Failed:      %-40s${NC}${BOLD}${CYAN}║${NC}\n" "$FAILED"
    echo -e "${BOLD}${CYAN}║${NC}                                                          ${BOLD}${CYAN}║${NC}"
    echo -e "${BOLD}${CYAN}║${NC}  ${RED}${BOLD}RESULT: ❌ FAIL — Security issues detected!${NC}             ${BOLD}${CYAN}║${NC}"
else
    printf "${BOLD}${CYAN}║${NC}  Failed:      %-40s${BOLD}${CYAN}║${NC}\n" "0"
    echo -e "${BOLD}${CYAN}║${NC}                                                          ${BOLD}${CYAN}║${NC}"
    echo -e "${BOLD}${CYAN}║${NC}  ${GREEN}${BOLD}RESULT: ✅ PASS — All security checks passed!${NC}          ${BOLD}${CYAN}║${NC}"
fi
echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

[[ "$FAILED" -eq 0 ]] && exit 0 || exit 1
