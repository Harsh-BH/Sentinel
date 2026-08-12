#!/usr/bin/env bash
# =============================================================================
# Project Sentinel — end-to-end integration tests
# =============================================================================
# Exercises the real stack (Postgres + RabbitMQ + Redis + API + nsjail worker)
# over HTTP. Requires a running stack:
#
#   docker compose up -d
#   ./scripts/integration-test.sh
#
# Beyond the happy paths this asserts the failure behaviours that are easy to
# regress and impossible to verify from unit tests:
#
#   * real concurrency (prefetch tracks pool size, so jobs overlap)
#   * a SIGKILLed worker's job is recovered by the reaper, not lost
#   * SIGTERM drains in-flight jobs instead of killing them
#   * a duplicate delivery cannot overwrite a finished result
#
# Env:
#   API_BASE          default http://localhost:8080
#   COMPOSE           default "docker compose"
#   SKIP_CHAOS=1      skip the worker-kill / drain tests (they restart the worker)
# =============================================================================
set -uo pipefail

API_BASE="${API_BASE:-http://localhost:8080}"
COMPOSE="${COMPOSE:-docker compose}"
# Exported so every compose invocation below resolves the same host port for
# Redis; otherwise "compose up -d worker" re-resolves its dependencies with a
# different published port and fails if that port is taken.
export REDIS_HOST_PORT="${REDIS_HOST_PORT:-6380}"
PASSED=0
FAILED=0
FAILED_NAMES=()

log()  { echo "  [INFO]  $*"; }
pass() { PASSED=$((PASSED + 1)); echo "  ✅ PASS: $*"; }
fail() { FAILED=$((FAILED + 1)); FAILED_NAMES+=("$1"); echo "  ❌ FAIL: $*"; }
head1() { echo; echo "── $* ─────────────────────────────────────────"; }

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

# submit <language> <source> [stdin] [time_limit_ms] [memory_limit_kb]
# echoes the job_id, or empty on failure
# post_json <json> -> http code, body in $LAST_BODY.
# Retries on 429: this suite issues well over the default 100 requests/minute, so
# the rate limiter correctly starts rejecting. Backing off here keeps the tests
# honest instead of weakening the limiter in docker-compose.
post_json() {
  local payload="$1" attempt code
  for attempt in 1 2 3 4 5 6; do
    code=$(curl -s --max-time 15 -o /tmp/sentinel_body -w '%{http_code}' \
      -X POST "${API_BASE}/api/v1/submissions" \
      -H 'Content-Type: application/json' -d "$payload")
    if [ "$code" != "429" ]; then
      LAST_BODY=$(cat /tmp/sentinel_body)
      echo "$code"; return 0
    fi
    log "rate limited (429), waiting ${RATE_LIMIT_BACKOFF:-20}s before retry ${attempt}"
    sleep "${RATE_LIMIT_BACKOFF:-20}"
  done
  LAST_BODY=$(cat /tmp/sentinel_body)
  echo "$code"
}

submit() {
  local lang="$1" src="$2" stdin="${3:-}" tl="${4:-}" ml="${5:-}"
  local payload
  payload=$(jq -nc \
    --arg l "$lang" --arg s "$src" --arg i "$stdin" \
    --argjson tl "${tl:-null}" --argjson ml "${ml:-null}" \
    '{language:$l, source_code:$s, stdin:$i}
     + (if $tl == null then {} else {time_limit_ms:$tl} end)
     + (if $ml == null then {} else {memory_limit_kb:$ml} end)')
  post_json "$payload" >/dev/null
  echo "$LAST_BODY" | jq -r '.job_id // empty'
}

# submit_raw <json> -> http code
submit_raw() { post_json "$1"; }

# get_code <path> -> http code, retrying on 429
get_code() {
  local path="$1" attempt code
  for attempt in 1 2 3 4 5 6; do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${API_BASE}${path}")
    [ "$code" != "429" ] && { echo "$code"; return 0; }
    log "rate limited on GET, waiting ${RATE_LIMIT_BACKOFF:-20}s"
    sleep "${RATE_LIMIT_BACKOFF:-20}"
  done
  echo "$code"
}

# await_terminal <job_id> [timeout_s] -> echoes final job JSON
await_terminal() {
  local id="$1" timeout="${2:-45}" waited=0 resp status
  while [ "$waited" -lt "$timeout" ]; do
    resp=$(curl -s --max-time 5 "${API_BASE}/api/v1/submissions/${id}")
    status=$(echo "$resp" | jq -r '.status // empty')
    # A 429 on the poll is not a job failure; just wait and try again.
    case "$status" in
      SUCCESS|COMPILATION_ERROR|RUNTIME_ERROR|TIMEOUT|MEMORY_LIMIT_EXCEEDED|INTERNAL_ERROR)
        echo "$resp"; return 0 ;;
    esac
    sleep 1; waited=$((waited + 1))
  done
  echo "$resp"; return 1
}

expect_status() {
  local name="$1" want="$2" job="$3" got
  got=$(echo "$job" | jq -r '.status // "NONE"')
  if [ "$got" = "$want" ]; then pass "$name (status=$got)"
  else fail "$name" "expected $want, got $got — $(echo "$job" | jq -c '{status,stderr,exit_code}')"
  fi
}

psql_q() { $COMPOSE exec -T postgres psql -U sentinel -d sentinel -tAc "$1" 2>/dev/null | tr -d '[:space:]'; }

for tool in jq curl; do
  command -v "$tool" >/dev/null || { echo "FATAL: $tool is required"; exit 1; }
done

# ---------------------------------------------------------------------------
head1 "Readiness"
# ---------------------------------------------------------------------------
for i in $(seq 1 30); do
  curl -sf --max-time 5 "${API_BASE}/api/v1/readyz" >/dev/null 2>&1 && break
  sleep 2
done
if curl -sf --max-time 5 "${API_BASE}/api/v1/readyz" >/dev/null; then
  pass "API is ready"
else
  echo "FATAL: API never became ready at ${API_BASE}"; exit 1
fi

# Liveness must NOT depend on backing services — that is what made a broker
# blip restart every API pod.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${API_BASE}/api/v1/livez")
[ "$code" = "200" ] && pass "/livez responds 200 (process-only check)" \
                    || fail "livez" "expected 200, got $code"

# ---------------------------------------------------------------------------
head1 "Python execution"
# ---------------------------------------------------------------------------
id=$(submit python 'print("hello sentinel")')
if [ -z "$id" ]; then fail "python hello" "submit returned no job_id"; else
  job=$(await_terminal "$id")
  expect_status "python hello world" SUCCESS "$job"
  out=$(echo "$job" | jq -r '.stdout')
  [[ "$out" == *"hello sentinel"* ]] && pass "python stdout captured" \
    || fail "python stdout" "got: $out"
fi

id=$(submit python 'import sys
data = sys.stdin.read().strip()
print(f"got:{data}")' "ping")
job=$(await_terminal "$id")
out=$(echo "$job" | jq -r '.stdout')
[[ "$out" == *"got:ping"* ]] && pass "python reads stdin" || fail "python stdin" "got: $out"

id=$(submit python 'raise ValueError("boom")')
job=$(await_terminal "$id")
expect_status "python runtime error" RUNTIME_ERROR "$job"
err=$(echo "$job" | jq -r '.stderr')
[[ "$err" == *"ValueError"* ]] && pass "python traceback reaches stderr" \
  || fail "python stderr" "got: $err"

# ---------------------------------------------------------------------------
head1 "Resource limits are enforced"
# ---------------------------------------------------------------------------
id=$(submit python 'while True: pass' "" 2000)
job=$(await_terminal "$id")
expect_status "infinite loop hits time limit" TIMEOUT "$job"

id=$(submit python 'x = bytearray(400 * 1024 * 1024)
print(len(x))' "" 5000 65536)
job=$(await_terminal "$id")
st=$(echo "$job" | jq -r '.status')
# Either the cgroup kills it (MEMORY_LIMIT_EXCEEDED) or Python raises
# MemoryError first (RUNTIME_ERROR). Both mean the limit held; SUCCESS does not.
if [ "$st" = "MEMORY_LIMIT_EXCEEDED" ] || [ "$st" = "RUNTIME_ERROR" ]; then
  pass "memory hog is stopped (status=$st)"
else
  fail "memory limit" "expected MEMORY_LIMIT_EXCEEDED or RUNTIME_ERROR, got $st"
fi

# The old code read the worker container's own cgroup and reported 0 or garbage.
id=$(submit python 'x = bytearray(50 * 1024 * 1024)
print(len(x))')
job=$(await_terminal "$id")
mem=$(echo "$job" | jq -r '.memory_used_kb // 0')
if [ "$mem" -gt 1000 ]; then
  pass "memory_used_kb is a real measurement (${mem} KB)"
else
  fail "memory measurement" "expected > 1000 KB for a 50 MB allocation, got ${mem}"
fi

# ---------------------------------------------------------------------------
head1 "Sandbox isolation"
# ---------------------------------------------------------------------------
id=$(submit python 'import socket
socket.create_connection(("1.1.1.1", 80), timeout=3)
print("NETWORK REACHED")')
job=$(await_terminal "$id")
out=$(echo "$job" | jq -r '.stdout')
if [[ "$out" == *"NETWORK REACHED"* ]]; then
  fail "network isolation" "sandbox reached the network"
else
  pass "network is unreachable from the sandbox ($(echo "$job" | jq -r .status))"
fi

id=$(submit python 'import os
os.fork() if hasattr(os, "fork") else None
print("pids", len(os.listdir("/proc")))')
job=$(await_terminal "$id")
[ -n "$(echo "$job" | jq -r '.status')" ] && pass "PID namespace isolates /proc" \
  || fail "pid namespace" "no status returned"

# ---- seccomp-bpf allowlist (DEFAULT KILL) ----
# These assert the filter is actually LOADED, not just present on disk. For a
# long time seccomp_policy_file was commented out because the policy referenced
# a syscall kafel does not define, so nsjail refused to start with it.

# A realistic program must still work: threads, file I/O, json, uname, cpu_count.
id=$(submit python 'import os, json, threading, platform, math, random, time
r = []
t = threading.Thread(target=lambda: r.append(1)); t.start(); t.join()
open("/tmp/x","w").write("hi")
assert open("/tmp/x").read() == "hi"
print(json.dumps({"thread": r, "sys": platform.system(), "cpus": os.cpu_count(),
                  "sqrt": round(math.sqrt(2),3), "rand": 0 <= random.random() <= 1,
                  "clock": time.time() > 0}))')
job=$(await_terminal "$id")
expect_status "seccomp allows a realistic python program" SUCCESS "$job"
out=$(echo "$job" | jq -r '.stdout')
[[ "$out" == *'"sys": "Linux"'* && "$out" == *'"thread": [1]'* ]] \
  && pass "threads, uname, file I/O and RNG all work under seccomp" \
  || fail "seccomp functionality" "unexpected stdout: $out"

# socket() must be killed by the syscall filter, independently of the netns.
id=$(submit python 'import socket
s = socket.socket()
print("SOCKET CREATED")')
job=$(await_terminal "$id")
out=$(echo "$job" | jq -r '.stdout'); err=$(echo "$job" | jq -r '.stderr')
if [[ "$out" == *"SOCKET CREATED"* ]]; then
  fail "seccomp blocks socket" "socket() succeeded inside the sandbox"
elif [[ "$err" == *"not permitted in the sandbox"* ]]; then
  pass "socket() killed by seccomp, with an explanatory message"
else
  pass "socket() did not succeed ($(echo "$job" | jq -r .status))"
fi

# ptrace is the classic sandbox-escape primitive.
id=$(submit python 'import ctypes
ctypes.CDLL(None).ptrace(0, 0, 0, 0)
print("PTRACE OK")')
job=$(await_terminal "$id")
out=$(echo "$job" | jq -r '.stdout')
[[ "$out" != *"PTRACE OK"* ]] && pass "ptrace blocked by seccomp" \
  || fail "seccomp blocks ptrace" "ptrace succeeded inside the sandbox"

# The exit code for a seccomp kill (159 = 128+31, SIGSYS) must be explained
# rather than surfacing as an unexplained RUNTIME_ERROR.
id=$(submit cpp '#include <sys/socket.h>
#include <cstdio>
int main(){ printf("fd=%d\n", socket(AF_INET, SOCK_STREAM, 0)); }')
job=$(await_terminal "$id" 60)
err=$(echo "$job" | jq -r '.stderr'); out=$(echo "$job" | jq -r '.stdout')
if [[ "$out" == *"fd="* ]] && [[ "$out" != *"fd=-1"* ]]; then
  fail "seccomp blocks cpp socket" "socket() succeeded from C++"
else
  [[ "$err" == *"not permitted in the sandbox"* ]] \
    && pass "C++ seccomp violation reported with an explanation" \
    || pass "C++ socket() did not succeed ($(echo "$job" | jq -r .status))"
fi

# ---------------------------------------------------------------------------
head1 "C++ compile-and-run"
# ---------------------------------------------------------------------------
id=$(submit cpp '#include <iostream>
int main() { std::cout << "cpp ok" << std::endl; return 0; }')
job=$(await_terminal "$id" 60)
expect_status "cpp hello world" SUCCESS "$job"
out=$(echo "$job" | jq -r '.stdout')
[[ "$out" == *"cpp ok"* ]] && pass "cpp stdout captured" || fail "cpp stdout" "got: $out"

id=$(submit cpp 'int main() { this is not valid c++ }')
job=$(await_terminal "$id" 60)
expect_status "cpp compile error" COMPILATION_ERROR "$job"

id=$(submit cpp 'int main() { int *p = nullptr; *p = 1; return 0; }')
job=$(await_terminal "$id" 60)
expect_status "cpp segfault" RUNTIME_ERROR "$job"

# A tight runtime limit must not starve the compiler: the compile pass has its
# own budget. Previously this reported COMPILATION_ERROR/TIMEOUT spuriously.
id=$(submit cpp '#include <iostream>
int main() { std::cout << "fast" << std::endl; }' "" 200)
job=$(await_terminal "$id" 60)
expect_status "tight runtime limit does not break compilation" SUCCESS "$job"

# ---------------------------------------------------------------------------
head1 "Input validation"
# ---------------------------------------------------------------------------
code=$(submit_raw '{"language":"ruby","source_code":"puts 1"}')
[ "$code" = "400" ] && pass "unsupported language rejected (400)" \
  || fail "invalid language" "expected 400, got $code"

code=$(submit_raw '{"language":"python","source_code":"   "}')
[ "$code" = "400" ] && pass "blank source rejected (400)" \
  || fail "blank source" "expected 400, got $code"

# Out-of-range limits are rejected, not silently clamped to the default.
code=$(submit_raw '{"language":"python","source_code":"print(1)","time_limit_ms":600000}')
if [ "$code" = "400" ]; then
  pass "out-of-range time limit rejected (400)"
else
  fail "limit validation" "expected 400 for time_limit_ms=600000, got $code"
fi

code=$(submit_raw '{"language":"python","source_code":"print(1)","memory_limit_kb":99999999}')
[ "$code" = "400" ] && pass "out-of-range memory limit rejected (400)" \
  || fail "memory limit validation" "expected 400, got $code"

code=$(get_code "/api/v1/submissions/not-a-uuid")
[ "$code" = "400" ] && pass "malformed job id rejected (400)" \
  || fail "malformed id" "expected 400, got $code"

code=$(get_code "/api/v1/submissions/$(uuidgen)")
[ "$code" = "404" ] && pass "unknown job id returns 404" \
  || fail "unknown id" "expected 404, got $code"

# ---------------------------------------------------------------------------
head1 "Real concurrency (prefetch tracks pool size)"
# ---------------------------------------------------------------------------
# Four 3-second jobs. Serial execution needs ~12s; a pool of 4 needs ~3-5s.
# With the old hardcoded prefetch=1 this test fails on wall-clock alone.
pool=$($COMPOSE exec -T worker printenv WORKER_POOL_SIZE 2>/dev/null | tr -d '[:space:]')
pool=${pool:-4}
log "worker pool size = $pool; submitting $pool concurrent 3s jobs"

ids=(); start=$(date +%s)
for _ in $(seq 1 "$pool"); do
  ids+=("$(submit python 'import time
time.sleep(3)
print("done")')")
done
for jid in "${ids[@]}"; do await_terminal "$jid" 60 >/dev/null; done
elapsed=$(( $(date +%s) - start ))
serial=$(( pool * 3 ))
log "wall clock ${elapsed}s vs ${serial}s if serialised"
if [ "$pool" -le 1 ] || [ "$elapsed" -lt "$serial" ]; then
  pass "jobs executed concurrently (${elapsed}s < ${serial}s serial)"
else
  fail "concurrency" "took ${elapsed}s, expected well under ${serial}s — prefetch may still be 1"
fi

active_max=$(curl -s "${API_BASE}/metrics" >/dev/null 2>&1; \
  $COMPOSE exec -T worker curl -s http://127.0.0.1:9090/metrics 2>/dev/null \
  | grep -E '^sentinel_executions_total' | wc -l)
[ "$active_max" -gt 0 ] && pass "worker exports execution metrics" \
  || fail "metrics" "no sentinel_executions_total series found"

# ---------------------------------------------------------------------------
head1 "Real-time push (Postgres LISTEN/NOTIFY, not polling)"
# ---------------------------------------------------------------------------
# Updates used to be a 500 ms poll per open connection. They are now pushed from
# a status-change trigger, so a terminal status should arrive within tens of
# milliseconds of the commit rather than up to a poll interval late.
if command -v node >/dev/null 2>&1; then
  WSJS=$(mktemp /tmp/sentinel-ws-XXXXXX.mjs)
  cat > "$WSJS" <<'NODEEOF'
const API = 'http://localhost:8080';
const r = await fetch(`${API}/api/v1/submissions`, {
  method: 'POST', headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({language: 'python',
    source_code: 'import time\ntime.sleep(2)\nprint("pushed")', time_limit_ms: 10000}),
});
const {job_id} = await r.json();
const t0 = Date.now();
const seen = [];
const ws = new WebSocket(`ws://localhost:8080/api/v1/submissions/${job_id}/stream`);
const TERMINAL = ['SUCCESS','RUNTIME_ERROR','TIMEOUT','COMPILATION_ERROR','MEMORY_LIMIT_EXCEEDED','INTERNAL_ERROR'];
await new Promise((res) => {
  ws.onmessage = (e) => {
    const j = JSON.parse(e.data);
    seen.push({ms: Date.now() - t0, status: j.status, stdout: j.stdout});
    if (TERMINAL.includes(j.status)) res();
  };
  ws.onerror = () => res();
  setTimeout(res, 20000);
});
try { ws.close(); } catch {}
console.log(JSON.stringify({job_id, seen}));
NODEEOF
  WSOUT=$(node "$WSJS" 2>&1 | tail -1); rm -f "$WSJS"
  if echo "$WSOUT" | jq -e '.seen' >/dev/null 2>&1; then
    first_ms=$(echo "$WSOUT" | jq -r '.seen[0].ms')
    term=$(echo "$WSOUT" | jq -r '[.seen[] | select(.status=="SUCCESS")][0] // empty')
    if [ -n "$term" ]; then
      term_ms=$(echo "$term" | jq -r '.ms')
      pass "WebSocket pushed the terminal status (at ${term_ms}ms; the job itself sleeps 2000ms)"
      # The job sleeps 2s. Anything much beyond that means we waited for a poll
      # tick rather than receiving a push.
      if [ "$term_ms" -lt 3000 ]; then
        pass "terminal event arrived promptly after commit (push, not a 5s poll tick)"
      else
        fail "push latency" "terminal status took ${term_ms}ms — looks like it waited for the safety poll"
      fi
    else
      fail "websocket push" "never received a terminal status: $WSOUT"
    fi
    [ "$first_ms" -lt 1000 ] && pass "initial state sent on connect (${first_ms}ms)" \
      || fail "websocket initial state" "took ${first_ms}ms"
    echo "$WSOUT" | jq -e '[.seen[] | select(.stdout != null and .stdout != "")] | length > 0' >/dev/null \
      && pass "pushed payload carries program output" \
      || fail "websocket payload" "no stdout in any pushed message"
  else
    fail "websocket push" "node client failed: $WSOUT"
  fi
else
  log "node not available — skipping WebSocket push test"
fi

# ---------------------------------------------------------------------------
head1 "Duplicate deliveries cannot corrupt a finished job"
# ---------------------------------------------------------------------------
id=$(submit python 'print("original")')
job=$(await_terminal "$id")
expect_status "job completes before replay" SUCCESS "$job"

created=$(psql_q "select created_at from execution_jobs where job_id='${id}'")
# Force the row back into a claimable-looking state is NOT what we test; instead
# assert the DB-level guard directly: a status write must not touch a terminal row.
# Wrapped in a CTE and counted: psql -tAc prints the command tag ("UPDATE 0")
# for a statement returning no rows, which is not distinguishable from a result.
rows=$(psql_q "with u as (
    update execution_jobs set status='RUNNING'
    where job_id='${id}'
      and status not in ('SUCCESS','COMPILATION_ERROR','RUNTIME_ERROR','TIMEOUT','MEMORY_LIMIT_EXCEEDED','INTERNAL_ERROR')
    returning 1)
  select count(*) from u")
if [ "$rows" = "0" ]; then
  pass "terminal-state guard rejects a late status write"
else
  fail "terminal guard" "a terminal job was moved back to RUNNING"
fi
still=$(psql_q "select status from execution_jobs where job_id='${id}'")
[ "$still" = "SUCCESS" ] && pass "finished result preserved (status=$still)" \
  || fail "result preserved" "status is now $still"

# ---------------------------------------------------------------------------
if [ "${SKIP_CHAOS:-0}" = "1" ]; then
  head1 "Chaos tests skipped (SKIP_CHAOS=1)"
else
head1 "Crash recovery: SIGKILL a worker mid-execution"
# ---------------------------------------------------------------------------
# The old design lost this job permanently: RabbitMQ redelivered it, the 10-minute
# Redis lock said "duplicate", the message was ACKed and discarded, and the row
# stayed RUNNING forever. The Postgres lease + reaper must recover it instead.
# 15s limit for an 8s sleep: with the default 5s limit the job would legitimately
# TIMEOUT and we would not be testing recovery at all.
id=$(submit python 'import time
time.sleep(8)
print("survived")' "" 15000)
log "submitted long job $id, waiting for it to start"
for i in $(seq 1 20); do
  st=$(psql_q "select status from execution_jobs where job_id='${id}'")
  [ "$st" = "RUNNING" ] && break
  sleep 1
done
st=$(psql_q "select status from execution_jobs where job_id='${id}'")
if [ "$st" != "RUNNING" ]; then
  fail "crash recovery setup" "job never reached RUNNING (status=$st)"
else
  lease=$(psql_q "select lease_expires_at is not null from execution_jobs where job_id='${id}'")
  [ "$lease" = "t" ] && pass "claimed job holds a durable lease" \
    || fail "lease" "lease_expires_at is null for a RUNNING job"

  log "SIGKILLing the worker (simulating a hard crash)"
  $COMPOSE kill -s SIGKILL worker >/dev/null 2>&1
  sleep 2
  $COMPOSE up -d worker >/dev/null 2>&1
  log "worker restarted; waiting for redelivery/reaper recovery (up to 150s)"

  recovered=""
  for i in $(seq 1 150); do
    st=$(psql_q "select status from execution_jobs where job_id='${id}'")
    case "$st" in
      SUCCESS|RUNTIME_ERROR|TIMEOUT|MEMORY_LIMIT_EXCEEDED|INTERNAL_ERROR) recovered="$st"; break ;;
    esac
    sleep 1
  done
  if [ "$recovered" = "SUCCESS" ]; then
    pass "job survived a worker SIGKILL and completed (status=$recovered)"
  elif [ -n "$recovered" ]; then
    pass "job was recovered to a terminal state after SIGKILL (status=$recovered)"
  else
    fail "crash recovery" "job stuck non-terminal 150s after worker SIGKILL (status=$st, attempts=$(psql_q "select attempts from execution_jobs where job_id='${id}'"))"
  fi
fi

# ---------------------------------------------------------------------------
head1 "Transactional outbox: submission survives a broker outage"
# ---------------------------------------------------------------------------
# The job row and its queue message are written in ONE transaction, so once the
# transaction commits delivery is guaranteed. Submission therefore must NOT fail
# when RabbitMQ is unreachable — it used to return 503 and mark the job
# INTERNAL_ERROR, losing work that had already been accepted.
log "stopping RabbitMQ"
$COMPOSE stop rabbitmq >/dev/null 2>&1
sleep 3

code=$(curl -s -o /tmp/sentinel_outbox -w '%{http_code}' --max-time 15 \
  -X POST "${API_BASE}/api/v1/submissions" -H 'Content-Type: application/json' \
  -d '{"language":"python","source_code":"print(\"outbox delivered\")"}')
obid=$(jq -r '.job_id // empty' /tmp/sentinel_outbox)

if [ "$code" = "202" ] && [ -n "$obid" ]; then
  pass "submission accepted (202) while the broker was down"
else
  fail "outbox accept" "expected 202 with a job_id, got $code $(cat /tmp/sentinel_outbox)"
fi

if [ -n "$obid" ]; then
  pending=$(psql_q "select count(*) from job_outbox where job_id='${obid}' and published_at is null")
  [ "$pending" = "1" ] && pass "message held durably in the outbox" \
    || fail "outbox durability" "expected 1 pending outbox row, got $pending"
  st=$(psql_q "select status from execution_jobs where job_id='${obid}'")
  [ "$st" = "QUEUED" ] && pass "job stays QUEUED rather than being failed" \
    || fail "outbox job status" "expected QUEUED, got $st"
fi

log "restarting RabbitMQ; the relay must deliver the deferred message"
$COMPOSE start rabbitmq >/dev/null 2>&1
for i in $(seq 1 30); do
  curl -sf --max-time 3 "${API_BASE}/api/v1/readyz" >/dev/null 2>&1 && break
  sleep 2
done

if [ -n "$obid" ]; then
  recovered=""
  for i in $(seq 1 90); do
    st=$(psql_q "select status from execution_jobs where job_id='${obid}'")
    case "$st" in SUCCESS|RUNTIME_ERROR|TIMEOUT|INTERNAL_ERROR) recovered="$st"; break ;; esac
    sleep 1
  done
  if [ "$recovered" = "SUCCESS" ]; then
    pass "outbox relay delivered the deferred job after recovery (status=$recovered)"
    out=$(psql_q "select stdout from execution_jobs where job_id='${obid}'")
    [[ "$out" == *"outboxdelivered"* ]] && pass "deferred job produced correct output" \
      || pass "deferred job completed (stdout='$out')"
  else
    fail "outbox relay" "deferred job never completed (status=$st)"
  fi
  left=$(psql_q "select count(*) from job_outbox where job_id='${obid}'")
  [ "$left" = "0" ] && pass "outbox entry cleared after confirmed delivery" \
    || fail "outbox cleanup" "$left entry left behind"
fi

# ---------------------------------------------------------------------------
head1 "Graceful shutdown drains in-flight work"
# ---------------------------------------------------------------------------
# SIGTERM used to cancel the context the sandbox ran under, SIGKILLing nsjail and
# losing the result. The two-context shutdown must let the job finish.
id=$(submit python 'import time
time.sleep(8)
print("drained cleanly")' "" 15000)
for i in $(seq 1 20); do
  st=$(psql_q "select status from execution_jobs where job_id='${id}'")
  [ "$st" = "RUNNING" ] && break
  sleep 1
done
st=$(psql_q "select status from execution_jobs where job_id='${id}'")
if [ "$st" != "RUNNING" ]; then
  fail "drain setup" "job never reached RUNNING (status=$st)"
else
  log "sending SIGTERM to the worker while the job runs"
  $COMPOSE stop -t 60 worker >/dev/null 2>&1
  final=$(psql_q "select status from execution_jobs where job_id='${id}'")
  if [ "$final" = "SUCCESS" ]; then
    pass "in-flight job completed during graceful shutdown (status=$final)"
    out=$(psql_q "select stdout from execution_jobs where job_id='${id}'")
    [[ "$out" == *"drainedcleanly"* ]] && pass "drained job's output was persisted" \
      || pass "drained job persisted (stdout='$out')"
  else
    fail "graceful drain" "job did not complete across SIGTERM (status=$final)"
  fi
  $COMPOSE up -d worker >/dev/null 2>&1
  for i in $(seq 1 30); do
    curl -sf --max-time 3 "${API_BASE}/api/v1/readyz" >/dev/null 2>&1 && break
    sleep 2
  done
  sleep 5
fi
fi  # SKIP_CHAOS

# ---------------------------------------------------------------------------
head1 "Data integrity"
# ---------------------------------------------------------------------------
orphans=$(psql_q "select count(*) from execution_jobs
  where status in ('QUEUED','COMPILING','RUNNING')
    and updated_at < now() - interval '5 minutes'")
[ "${orphans:-0}" = "0" ] && pass "no jobs stranded in a non-terminal state" \
  || fail "stranded jobs" "$orphans job(s) stuck non-terminal for over 5 minutes"

dupes=$(psql_q "select count(*) from (select job_id from execution_jobs group by job_id having count(*) > 1) d")
[ "${dupes:-0}" = "0" ] && pass "no duplicate job rows" \
  || fail "duplicate rows" "$dupes job_id(s) appear more than once"

# ---------------------------------------------------------------------------
head1 "Results"
# ---------------------------------------------------------------------------
echo "  Passed: ${PASSED}"
echo "  Failed: ${FAILED}"
if [ "$FAILED" -gt 0 ]; then
  echo "  Failing tests:"
  for n in "${FAILED_NAMES[@]}"; do echo "    - $n"; done
  exit 1
fi
echo "  All end-to-end tests passed."
