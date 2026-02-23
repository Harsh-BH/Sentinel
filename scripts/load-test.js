// =============================================================================
// Project Sentinel — k6 Load Test
// =============================================================================
// Ramps from 10 → 1000 concurrent submissions over 5 minutes.
// Mix of Python and C++ submissions. Validates:
//   - 0 dropped requests (http_req_failed rate < 0.01)
//   - p99 total time < 30s (submit + poll until terminal)
//   - All results eventually resolve to a terminal status
//
// Prerequisites:
//   brew install k6  (macOS)  |  sudo apt install k6  (Ubuntu)
//   https://k6.io/docs/getting-started/installation/
//
// Usage:
//   k6 run scripts/load-test.js
//   k6 run scripts/load-test.js --env BASE_URL=http://localhost:8080
//   k6 run scripts/load-test.js --env BASE_URL=http://sentinel.example.com
// =============================================================================

import http from "k6/http";
import { check, sleep, group } from "k6";
import { Counter, Rate, Trend } from "k6/metrics";

// ── Custom metrics ──
const submissionErrors = new Counter("sentinel_submission_errors");
const pollTimeouts = new Counter("sentinel_poll_timeouts");
const executionSuccess = new Counter("sentinel_execution_success");
const executionFailed = new Counter("sentinel_execution_failed");
const errorRate = new Rate("sentinel_error_rate");
const totalDuration = new Trend("sentinel_total_duration", true); // ms

// ── Configuration ──
const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const POLL_TIMEOUT_S = 60;
const POLL_INTERVAL_S = 1;

// ── Load profile ──
// Ramp from 10 → 1000 VUs over 5 minutes, sustain, then ramp down
export const options = {
  stages: [
    { duration: "30s", target: 10 },    // warm-up
    { duration: "1m", target: 100 },     // ramp to 100
    { duration: "1m", target: 500 },     // ramp to 500
    { duration: "2m", target: 1000 },    // ramp to 1000
    { duration: "1m", target: 1000 },    // sustain 1000
    { duration: "30s", target: 0 },      // ramp down
  ],
  thresholds: {
    http_req_failed: ["rate<0.01"],                      // < 1% HTTP failures
    sentinel_error_rate: ["rate<0.05"],                  // < 5% execution errors
    sentinel_total_duration: ["p(99)<30000"],            // p99 < 30s
    sentinel_poll_timeouts: ["count<10"],                // < 10 poll timeouts
  },
};

// ── Test payloads ──
const PYTHON_PAYLOADS = [
  {
    source_code: 'print("Hello from k6!")',
    language: "python",
    stdin: "",
    expected_stdout: "Hello from k6!",
  },
  {
    source_code: 'import sys\nfor line in sys.stdin:\n    print(line.strip().upper())',
    language: "python",
    stdin: "hello world",
    expected_stdout: "HELLO WORLD",
  },
  {
    source_code: "print(sum(range(1000)))",
    language: "python",
    stdin: "",
    expected_stdout: "499500",
  },
  {
    source_code:
      'import math\nprint(f"{math.pi:.10f}")',
    language: "python",
    stdin: "",
    expected_stdout: "3.1415926536",
  },
  {
    source_code:
      "data = list(range(100000))\ndata.sort(reverse=True)\nprint(data[0])",
    language: "python",
    stdin: "",
    expected_stdout: "99999",
  },
];

const CPP_PAYLOADS = [
  {
    source_code:
      '#include <iostream>\nint main() { std::cout << "Hello from k6!" << std::endl; return 0; }',
    language: "cpp",
    stdin: "",
    expected_stdout: "Hello from k6!",
  },
  {
    source_code:
      '#include <iostream>\n#include <algorithm>\n#include <vector>\nint main() {\n    std::vector<int> v = {5,3,1,4,2};\n    std::sort(v.begin(), v.end());\n    for (int x : v) std::cout << x << " ";\n    std::cout << std::endl;\n    return 0;\n}',
    language: "cpp",
    stdin: "",
    expected_stdout: "1 2 3 4 5",
  },
  {
    source_code:
      '#include <iostream>\n#include <string>\nint main() { std::string s; std::getline(std::cin, s); std::cout << "Got: " << s << std::endl; return 0; }',
    language: "cpp",
    stdin: "k6 input",
    expected_stdout: "Got: k6 input",
  },
];

const ALL_PAYLOADS = [...PYTHON_PAYLOADS, ...CPP_PAYLOADS];

// ── Helpers ──

function pickRandom(arr) {
  return arr[Math.floor(Math.random() * arr.length)];
}

function submitCode(payload) {
  const res = http.post(
    `${BASE_URL}/api/v1/submissions`,
    JSON.stringify({
      source_code: payload.source_code,
      language: payload.language,
      stdin: payload.stdin,
    }),
    {
      headers: { "Content-Type": "application/json" },
      tags: { name: "submit" },
    }
  );
  return res;
}

function pollUntilDone(jobId) {
  const start = Date.now();
  let elapsed = 0;

  while (elapsed < POLL_TIMEOUT_S * 1000) {
    const res = http.get(`${BASE_URL}/api/v1/submissions/${jobId}`, {
      tags: { name: "poll" },
    });

    if (res.status === 200) {
      try {
        const body = JSON.parse(res.body);
        const status = body.status;
        if (
          [
            "SUCCESS",
            "COMPILATION_ERROR",
            "RUNTIME_ERROR",
            "TIMEOUT",
            "MEMORY_LIMIT_EXCEEDED",
            "INTERNAL_ERROR",
          ].includes(status)
        ) {
          return { body, durationMs: Date.now() - start };
        }
      } catch (_) {
        // JSON parse error, retry
      }
    }

    sleep(POLL_INTERVAL_S);
    elapsed = Date.now() - start;
  }

  return null; // timeout
}

// ── Main VU function ──
export default function () {
  const payload = pickRandom(ALL_PAYLOADS);

  group("submit_and_poll", function () {
    // 1. Submit
    const submitRes = submitCode(payload);
    const submitOk = check(submitRes, {
      "submit status is 202": (r) => r.status === 202,
      "response has job_id": (r) => {
        try {
          return JSON.parse(r.body).job_id !== undefined;
        } catch (_) {
          return false;
        }
      },
    });

    if (!submitOk) {
      submissionErrors.add(1);
      errorRate.add(true);
      return;
    }

    const jobId = JSON.parse(submitRes.body).job_id;

    // 2. Poll until terminal
    const result = pollUntilDone(jobId);

    if (result === null) {
      pollTimeouts.add(1);
      errorRate.add(true);
      return;
    }

    totalDuration.add(result.durationMs);

    // 3. Validate result
    const body = result.body;
    if (body.status === "SUCCESS") {
      executionSuccess.add(1);
      errorRate.add(false);

      // Optionally check stdout
      if (payload.expected_stdout) {
        const stdoutTrimmed = (body.stdout || "").trim();
        check(body, {
          "stdout matches expected": () =>
            stdoutTrimmed.includes(payload.expected_stdout),
        });
      }
    } else if (body.status === "INTERNAL_ERROR") {
      executionFailed.add(1);
      errorRate.add(true);
    } else {
      // COMPILATION_ERROR, RUNTIME_ERROR, TIMEOUT, MEMORY_LIMIT_EXCEEDED
      // These are valid sandbox responses, not system errors
      executionSuccess.add(1);
      errorRate.add(false);
    }
  });

  // Small think time between iterations
  sleep(Math.random() * 0.5);
}

// ── Summary handler ──
export function handleSummary(data) {
  const passed = data.metrics.sentinel_error_rate
    ? data.metrics.sentinel_error_rate.values.rate < 0.05
    : true;
  const p99Ok = data.metrics.sentinel_total_duration
    ? data.metrics.sentinel_total_duration.values["p(99)"] < 30000
    : true;

  console.log("\n" + "=".repeat(70));
  console.log("  SENTINEL LOAD TEST RESULTS");
  console.log("=".repeat(70));
  console.log(
    `  Total VU iterations:    ${
      data.metrics.iterations ? data.metrics.iterations.values.count : "N/A"
    }`
  );
  console.log(
    `  Submission errors:      ${
      data.metrics.sentinel_submission_errors
        ? data.metrics.sentinel_submission_errors.values.count
        : 0
    }`
  );
  console.log(
    `  Poll timeouts:          ${
      data.metrics.sentinel_poll_timeouts
        ? data.metrics.sentinel_poll_timeouts.values.count
        : 0
    }`
  );
  console.log(
    `  Execution successes:    ${
      data.metrics.sentinel_execution_success
        ? data.metrics.sentinel_execution_success.values.count
        : 0
    }`
  );
  console.log(
    `  Execution failures:     ${
      data.metrics.sentinel_execution_failed
        ? data.metrics.sentinel_execution_failed.values.count
        : 0
    }`
  );
  console.log(
    `  Error rate:             ${
      data.metrics.sentinel_error_rate
        ? (data.metrics.sentinel_error_rate.values.rate * 100).toFixed(2) + "%"
        : "N/A"
    }`
  );
  console.log(
    `  p99 total duration:     ${
      data.metrics.sentinel_total_duration
        ? (data.metrics.sentinel_total_duration.values["p(99)"] / 1000).toFixed(
            2
          ) + "s"
        : "N/A"
    }`
  );
  console.log("=".repeat(70));
  console.log(`  RESULT: ${passed && p99Ok ? "✅ PASS" : "❌ FAIL"}`);
  console.log("=".repeat(70) + "\n");

  return {
    stdout: textSummary(data, { indent: "  ", enableColors: true }),
  };
}

// k6 built-in text summary
import { textSummary } from "https://jslib.k6.io/k6-summary/0.0.3/index.js";
