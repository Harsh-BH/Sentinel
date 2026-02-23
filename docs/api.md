# 📡 Sentinel API Reference

> Complete API documentation for the Sentinel Remote Code Execution Engine.

**Base URL**: `http://localhost:8080` (development) | `https://sentinel.yourdomain.com` (production)

---

## Table of Contents

- [Authentication](#authentication)
- [Rate Limiting](#rate-limiting)
- [Endpoints](#endpoints)
  - [Submit Code](#submit-code)
  - [Get Submission Result](#get-submission-result)
  - [Stream Submission Updates (WebSocket)](#stream-submission-updates-websocket)
  - [List Languages](#list-languages)
  - [Health Check](#health-check)
  - [Prometheus Metrics](#prometheus-metrics)
- [Data Models](#data-models)
- [Error Handling](#error-handling)
- [WebSocket Protocol](#websocket-protocol)
- [OpenAPI 3.0 Specification](#openapi-30-specification)

---

## Authentication

Sentinel does not currently require authentication. Rate limiting is enforced per-IP.

## Rate Limiting

| Endpoint Pattern | Limit |
|------------------|-------|
| `POST /api/v1/submissions` | 100 requests/minute per IP |
| `GET /api/v1/submissions/:id` | 100 requests/minute per IP |
| All other endpoints | Unlimited |

When rate-limited, the API returns `429 Too Many Requests`.

---

## Endpoints

### Submit Code

Submit source code for sandboxed execution.

```
POST /api/v1/submissions
Content-Type: application/json
```

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `language` | string | ✅ | Programming language (`python` or `cpp`) |
| `source_code` | string | ✅ | Source code to execute |
| `stdin` | string | ❌ | Standard input for the program |
| `time_limit_ms` | integer | ❌ | Time limit in milliseconds (default: 5000, max: 10000) |
| `memory_limit_kb` | integer | ❌ | Memory limit in KB (default: 262144 = 256MB) |

#### Example Request

```bash
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "source_code": "print(\"Hello, World!\")",
    "stdin": ""
  }'
```

#### Response — `202 Accepted`

```json
{
  "job_id": "01912345-6789-7abc-def0-123456789abc",
  "status": "QUEUED"
}
```

#### Error Responses

| Status | Condition | Body |
|--------|-----------|------|
| `400` | Invalid JSON, missing required fields, unsupported language, empty source code | `{"error": "Invalid language"}` |
| `413` | Payload too large (>64KB source code) | `{"error": "Payload too large"}` |
| `429` | Rate limit exceeded | `{"error": "Too many requests"}` |
| `503` | Failed to publish to message queue | `{"error": "Service temporarily unavailable"}` |
| `500` | Unexpected internal error | `{"error": "Internal server error"}` |

---

### Get Submission Result

Retrieve the current state and results of a submission.

```
GET /api/v1/submissions/:id
```

#### Path Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | UUID | The job ID returned from submission |

#### Example Request

```bash
curl http://localhost:8080/api/v1/submissions/01912345-6789-7abc-def0-123456789abc
```

#### Response — `200 OK`

```json
{
  "job_id": "01912345-6789-7abc-def0-123456789abc",
  "language": "python",
  "source_code": "print(\"Hello, World!\")",
  "stdin": "",
  "stdout": "Hello, World!\n",
  "stderr": "",
  "status": "SUCCESS",
  "exit_code": 0,
  "time_used_ms": 42,
  "memory_used_kb": 8192,
  "time_limit_ms": 5000,
  "memory_limit_kb": 262144,
  "created_at": "2026-02-20T10:00:00Z",
  "updated_at": "2026-02-20T10:00:01Z"
}
```

#### Error Responses

| Status | Condition | Body |
|--------|-----------|------|
| `400` | Invalid UUID format | `{"error": "Invalid job ID format"}` |
| `404` | Job not found | `{"error": "Job not found"}` |
| `429` | Rate limit exceeded | `{"error": "Too many requests"}` |
| `500` | Unexpected internal error | `{"error": "Internal server error"}` |

---

### Stream Submission Updates (WebSocket)

Open a WebSocket connection to receive real-time status updates for a submission.

```
GET /api/v1/submissions/:id/stream
Upgrade: websocket
Connection: Upgrade
```

See [WebSocket Protocol](#websocket-protocol) for full details.

#### Path Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `id` | UUID | The job ID to stream updates for |

#### Error Responses (before upgrade)

| Status | Condition | Body |
|--------|-----------|------|
| `400` | Invalid UUID format | `{"error": "Invalid job ID format"}` |
| `404` | Job not found | `{"error": "Job not found"}` |

---

### List Languages

Get the list of supported programming languages.

```
GET /api/v1/languages
```

#### Example Request

```bash
curl http://localhost:8080/api/v1/languages
```

#### Response — `200 OK`

```json
{
  "languages": [
    {
      "name": "python",
      "version": "3.12"
    },
    {
      "name": "cpp",
      "version": "17",
      "compiler": "g++ (GCC 13)"
    }
  ]
}
```

---

### Health Check

Check the health of the API and its dependencies.

```
GET /api/v1/health
```

#### Example Request

```bash
curl http://localhost:8080/api/v1/health
```

#### Response — `200 OK` (all healthy)

```json
{
  "status": "ok",
  "services": {
    "postgres": "ok",
    "rabbitmq": "ok",
    "redis": "ok"
  }
}
```

#### Response — `503 Service Unavailable` (degraded)

```json
{
  "status": "degraded",
  "services": {
    "postgres": "ok",
    "rabbitmq": "error: dial tcp 127.0.0.1:5672: connect: connection refused",
    "redis": "ok"
  }
}
```

---

### Prometheus Metrics

Exposes Prometheus-format metrics for scraping.

```
GET /metrics
```

Returns standard Go runtime metrics plus Gin HTTP request metrics. See the [Observability section](../README.md#observability-prometheus--grafana) for details.

---

## Data Models

### ExecutionStatus

The lifecycle state of a code execution job.

| Status | Terminal | Description |
|--------|----------|-------------|
| `QUEUED` | ❌ | Job received and waiting for a worker |
| `COMPILING` | ❌ | C++ source is being compiled |
| `RUNNING` | ❌ | Code is executing in the sandbox |
| `SUCCESS` | ✅ | Execution completed successfully (exit code 0) |
| `COMPILATION_ERROR` | ✅ | C++ compilation failed |
| `RUNTIME_ERROR` | ✅ | Program exited with non-zero exit code |
| `TIMEOUT` | ✅ | Execution exceeded the time limit |
| `MEMORY_LIMIT_EXCEEDED` | ✅ | Program exceeded the memory limit |
| `INTERNAL_ERROR` | ✅ | System-level failure (sandbox crash, etc.) |

### Job

Full representation of a code execution job.

| Field | Type | Description |
|-------|------|-------------|
| `job_id` | UUID | Unique identifier (UUIDv7) |
| `language` | string | `python` or `cpp` |
| `source_code` | string | Submitted source code |
| `stdin` | string | Standard input provided |
| `stdout` | string | Standard output (omitted if empty) |
| `stderr` | string | Standard error (omitted if empty) |
| `status` | ExecutionStatus | Current lifecycle state |
| `exit_code` | integer \| null | Process exit code (omitted until terminal) |
| `time_used_ms` | integer \| null | Wall-clock execution time in ms |
| `memory_used_kb` | integer \| null | Peak memory usage in KB |
| `time_limit_ms` | integer | Configured time limit |
| `memory_limit_kb` | integer | Configured memory limit |
| `created_at` | ISO 8601 | When the job was submitted |
| `updated_at` | ISO 8601 | When the job was last updated |

### SubmitRequest

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `language` | string | ✅ | — | `python` or `cpp` |
| `source_code` | string | ✅ | — | Source code to execute |
| `stdin` | string | ❌ | `""` | Standard input |
| `time_limit_ms` | integer | ❌ | 5000 | Time limit in milliseconds |
| `memory_limit_kb` | integer | ❌ | 262144 | Memory limit in KB |

### SubmitResponse

| Field | Type | Description |
|-------|------|-------------|
| `job_id` | UUID | Assigned job identifier |
| `status` | string | Always `"QUEUED"` on successful submission |

### LanguageInfo

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Language identifier (`python`, `cpp`) |
| `version` | string | Language/compiler version |
| `compiler` | string | Compiler info (omitted for interpreted languages) |

### HealthResponse

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"ok"` or `"degraded"` |
| `services.postgres` | string | `"ok"` or error message |
| `services.rabbitmq` | string | `"ok"` or error message |
| `services.redis` | string | `"ok"` or error message |

### ErrorResponse

| Field | Type | Description |
|-------|------|-------------|
| `error` | string | Human-readable error message |

---

## Error Handling

All errors are returned as JSON with a single `error` field:

```json
{
  "error": "Human-readable error message"
}
```

### HTTP Status Code Summary

| Code | Meaning | When |
|------|---------|------|
| `200` | OK | Successful GET request |
| `202` | Accepted | Submission queued successfully |
| `400` | Bad Request | Invalid input (bad JSON, missing fields, invalid UUID) |
| `404` | Not Found | Job ID does not exist |
| `413` | Payload Too Large | Source code exceeds size limit |
| `429` | Too Many Requests | Rate limit exceeded |
| `500` | Internal Server Error | Unexpected server failure |
| `503` | Service Unavailable | Backend dependency down (health check or publish failed) |

---

## WebSocket Protocol

### Connection

```javascript
const ws = new WebSocket("ws://localhost:8080/api/v1/submissions/{job_id}/stream");
```

### Connection Parameters

| Parameter | Value |
|-----------|-------|
| Max connection duration | 5 minutes |
| Poll interval (server-side) | 500ms |
| Ping interval | 30s |
| Pong timeout | 10s |
| Max client message size | 512 bytes |

### Message Flow

1. **Client** connects via WebSocket upgrade
2. **Server** validates the job ID exists (returns 400/404 if not)
3. **Server** polls the database every 500ms for status changes
4. **Server** sends a JSON `Job` object whenever the status changes
5. **Server** closes the connection when the job reaches a terminal state
6. **Server** sends periodic pings to keep the connection alive

### Server → Client Messages

Each message is a full JSON `Job` object (see [Job model](#job)):

```json
{
  "job_id": "01912345-6789-7abc-def0-123456789abc",
  "language": "python",
  "source_code": "print('hello')",
  "stdin": "",
  "status": "RUNNING",
  "time_limit_ms": 5000,
  "memory_limit_kb": 262144,
  "created_at": "2026-02-20T10:00:00Z",
  "updated_at": "2026-02-20T10:00:00.5Z"
}
```

### Terminal States

The WebSocket connection is closed automatically when the job reaches any of these statuses:
- `SUCCESS`
- `COMPILATION_ERROR`
- `RUNTIME_ERROR`
- `TIMEOUT`
- `MEMORY_LIMIT_EXCEEDED`
- `INTERNAL_ERROR`

### Close Codes

| Code | Reason |
|------|--------|
| 1000 (Normal) | Job completed or max duration exceeded |
| 1006 (Abnormal) | Connection dropped unexpectedly |

### Client Example (JavaScript)

```javascript
function streamJob(jobId) {
  const ws = new WebSocket(`ws://localhost:8080/api/v1/submissions/${jobId}/stream`);

  ws.onopen = () => console.log("Connected, streaming updates...");

  ws.onmessage = (event) => {
    const job = JSON.parse(event.data);
    console.log(`Status: ${job.status}`);

    if (job.stdout) console.log(`Output: ${job.stdout}`);
    if (job.stderr) console.log(`Errors: ${job.stderr}`);
  };

  ws.onclose = (event) => {
    console.log(`Connection closed: ${event.reason || "done"}`);
  };

  ws.onerror = (error) => {
    console.error("WebSocket error:", error);
  };
}
```

---

## OpenAPI 3.0 Specification

```yaml
openapi: 3.0.3
info:
  title: Sentinel API
  description: Distributed Remote Code Execution Engine
  version: 1.0.0
  license:
    name: MIT
    url: https://opensource.org/licenses/MIT

servers:
  - url: http://localhost:8080
    description: Local development
  - url: https://sentinel.yourdomain.com
    description: Production

paths:
  /api/v1/submissions:
    post:
      summary: Submit code for execution
      operationId: submitCode
      tags: [Submissions]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/SubmitRequest"
            examples:
              python:
                summary: Python hello world
                value:
                  language: python
                  source_code: "print('Hello, World!')"
                  stdin: ""
              cpp:
                summary: C++ hello world
                value:
                  language: cpp
                  source_code: |
                    #include <iostream>
                    int main() { std::cout << "Hello, World!" << std::endl; return 0; }
                  stdin: ""
      responses:
        "202":
          description: Submission accepted and queued
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/SubmitResponse"
        "400":
          description: Invalid request
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorResponse"
              examples:
                invalidLanguage:
                  value: { "error": "Invalid language" }
                emptySource:
                  value: { "error": "Source code is required" }
        "413":
          description: Payload too large
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorResponse"
        "429":
          description: Rate limit exceeded
        "503":
          description: Service temporarily unavailable
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorResponse"

  /api/v1/submissions/{id}:
    get:
      summary: Get submission result
      operationId: getSubmission
      tags: [Submissions]
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            format: uuid
          description: Job ID (UUID)
      responses:
        "200":
          description: Job details
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Job"
        "400":
          description: Invalid job ID format
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorResponse"
        "404":
          description: Job not found
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorResponse"
        "429":
          description: Rate limit exceeded

  /api/v1/submissions/{id}/stream:
    get:
      summary: Stream submission updates via WebSocket
      operationId: streamSubmission
      tags: [Submissions]
      description: |
        Upgrades to a WebSocket connection. The server sends Job JSON objects
        whenever the status changes. The connection closes automatically when
        the job reaches a terminal state or after 5 minutes.
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            format: uuid
          description: Job ID (UUID)
      responses:
        "101":
          description: WebSocket upgrade successful
        "400":
          description: Invalid job ID format
        "404":
          description: Job not found

  /api/v1/languages:
    get:
      summary: List supported languages
      operationId: listLanguages
      tags: [Languages]
      responses:
        "200":
          description: List of supported languages
          content:
            application/json:
              schema:
                type: object
                properties:
                  languages:
                    type: array
                    items:
                      $ref: "#/components/schemas/LanguageInfo"

  /api/v1/health:
    get:
      summary: Health check
      operationId: healthCheck
      tags: [Health]
      responses:
        "200":
          description: All services healthy
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/HealthResponse"
        "503":
          description: One or more services degraded
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/HealthResponse"

  /metrics:
    get:
      summary: Prometheus metrics
      operationId: getMetrics
      tags: [Observability]
      responses:
        "200":
          description: Prometheus text format metrics
          content:
            text/plain:
              schema:
                type: string

components:
  schemas:
    SubmitRequest:
      type: object
      required: [language, source_code]
      properties:
        language:
          type: string
          enum: [python, cpp]
          description: Programming language
        source_code:
          type: string
          minLength: 1
          description: Source code to execute
        stdin:
          type: string
          default: ""
          description: Standard input for the program
        time_limit_ms:
          type: integer
          minimum: 1000
          maximum: 10000
          default: 5000
          description: Time limit in milliseconds
        memory_limit_kb:
          type: integer
          minimum: 1024
          maximum: 524288
          default: 262144
          description: Memory limit in kilobytes

    SubmitResponse:
      type: object
      properties:
        job_id:
          type: string
          format: uuid
          description: Assigned job identifier
        status:
          type: string
          enum: [QUEUED]
          description: Initial status (always QUEUED)

    Job:
      type: object
      properties:
        job_id:
          type: string
          format: uuid
        language:
          type: string
          enum: [python, cpp]
        source_code:
          type: string
        stdin:
          type: string
        stdout:
          type: string
          description: Standard output (omitted if empty)
        stderr:
          type: string
          description: Standard error (omitted if empty)
        status:
          $ref: "#/components/schemas/ExecutionStatus"
        exit_code:
          type: integer
          nullable: true
          description: Process exit code (null until terminal)
        time_used_ms:
          type: integer
          nullable: true
          description: Wall-clock execution time in ms
        memory_used_kb:
          type: integer
          nullable: true
          description: Peak memory usage in KB
        time_limit_ms:
          type: integer
        memory_limit_kb:
          type: integer
        created_at:
          type: string
          format: date-time
        updated_at:
          type: string
          format: date-time

    ExecutionStatus:
      type: string
      enum:
        - QUEUED
        - COMPILING
        - RUNNING
        - SUCCESS
        - COMPILATION_ERROR
        - RUNTIME_ERROR
        - TIMEOUT
        - MEMORY_LIMIT_EXCEEDED
        - INTERNAL_ERROR

    LanguageInfo:
      type: object
      properties:
        name:
          type: string
          enum: [python, cpp]
        version:
          type: string
        compiler:
          type: string
          description: Compiler info (omitted for interpreted languages)

    HealthResponse:
      type: object
      properties:
        status:
          type: string
          enum: [ok, degraded]
        services:
          type: object
          properties:
            postgres:
              type: string
            rabbitmq:
              type: string
            redis:
              type: string

    ErrorResponse:
      type: object
      properties:
        error:
          type: string
          description: Human-readable error message
```
