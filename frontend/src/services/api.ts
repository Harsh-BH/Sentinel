import axios from 'axios';
import type {
  ExecutionStatus,
  HealthResponse,
  Job,
  Language,
  LanguageInfo,
  SubmitRequest,
  SubmitResponse,
} from '../types/submission';

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 15000,
  headers: {
    'Content-Type': 'application/json',
  },
});

// Request interceptor — add request ID
api.interceptors.request.use((config) => {
  config.headers['X-Request-ID'] = crypto.randomUUID();
  return config;
});

// Response interceptor — unwrap errors
api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response) {
      const msg = error.response.data?.error || error.response.statusText;
      return Promise.reject(new Error(msg));
    }
    if (error.request) {
      return Promise.reject(new Error('Network error — server unreachable'));
    }
    return Promise.reject(error);
  },
);

// =============================================================================
// Backend ↔ frontend schema adapter
//
// The Go backend uses a different field set and status enum than the
// TypeScript types in src/types/submission.ts. Rather than refactoring every
// component, we translate at this single boundary so the rest of the app sees
// the shape it expects.
//
// Backend → frontend mapping:
//   job_id              → id
//   time_used_ms        → execution_time_ms
//   memory_used_kb      → memory_used_bytes  (× 1024)
//   COMPILING           → PROCESSING
//   COMPILATION_ERROR   → COMPILE_ERROR
//   MEMORY_LIMIT_EXCEEDED → MEMORY_LIMIT
//   INTERNAL_ERROR      → SYSTEM_ERROR
// =============================================================================

interface BackendJob {
  job_id: string;
  language: Language;
  source_code: string;
  stdin?: string;
  stdout?: string;
  stderr?: string;
  status: string;
  exit_code?: number | null;
  time_used_ms?: number | null;
  memory_used_kb?: number | null;
  time_limit_ms?: number;
  memory_limit_kb?: number;
  created_at: string;
  updated_at: string;
}

interface BackendSubmitResponse {
  job_id: string;
  status: string;
}

interface BackendLanguage {
  name: Language;
  version: string;
  compiler?: string;
}

interface BackendHealthResponse {
  status: string;
  services?: Record<string, string>;
}

const STATUS_MAP: Record<string, ExecutionStatus> = {
  QUEUED: 'QUEUED',
  COMPILING: 'PROCESSING',
  RUNNING: 'RUNNING',
  SUCCESS: 'SUCCESS',
  COMPILATION_ERROR: 'COMPILE_ERROR',
  RUNTIME_ERROR: 'RUNTIME_ERROR',
  TIMEOUT: 'TIMEOUT',
  MEMORY_LIMIT_EXCEEDED: 'MEMORY_LIMIT',
  INTERNAL_ERROR: 'SYSTEM_ERROR',
};

function mapStatus(s: string): ExecutionStatus {
  return STATUS_MAP[s] ?? 'SYSTEM_ERROR';
}

function adaptJob(b: BackendJob): Job {
  return {
    id: b.job_id,
    source_code: b.source_code,
    language: b.language,
    stdin: b.stdin ?? '',
    status: mapStatus(b.status),
    stdout: b.stdout ?? null,
    stderr: b.stderr ?? null,
    exit_code: b.exit_code ?? null,
    execution_time_ms: b.time_used_ms ?? null,
    memory_used_bytes:
      b.memory_used_kb != null ? b.memory_used_kb * 1024 : null,
    created_at: b.created_at,
    updated_at: b.updated_at,
  };
}

function adaptSubmitResponse(b: BackendSubmitResponse): SubmitResponse {
  return {
    id: b.job_id,
    status: mapStatus(b.status),
    // Backend doesn't return created_at on 202; synthesize for the UI.
    created_at: new Date().toISOString(),
  };
}

const FILE_EXTENSIONS: Record<Language, string> = {
  python: '.py',
  cpp: '.cpp',
};

const LANGUAGE_LABELS: Record<Language, string> = {
  python: 'Python',
  cpp: 'C++',
};

const MONACO_IDS: Record<Language, string> = {
  python: 'python',
  cpp: 'cpp',
};

function adaptLanguage(b: BackendLanguage): LanguageInfo {
  const name = LANGUAGE_LABELS[b.name] ?? String(b.name);
  return {
    id: b.name,
    name: b.compiler ? `${name} (${b.compiler})` : `${name} ${b.version}`,
    version: b.version,
    file_extension: FILE_EXTENSIONS[b.name] ?? '',
    monaco_id: MONACO_IDS[b.name] ?? String(b.name),
  };
}

// =============================================================================
// Public API
// =============================================================================

/** Submit code for execution */
export async function submitCode(req: SubmitRequest): Promise<SubmitResponse> {
  const { data } = await api.post<BackendSubmitResponse>('/submissions', req);
  return adaptSubmitResponse(data);
}

/** Get job by ID (polling fallback) */
export async function getJob(id: string): Promise<Job> {
  const { data } = await api.get<BackendJob>(`/submissions/${id}`);
  return adaptJob(data);
}

/** List supported languages */
export async function getLanguages(): Promise<LanguageInfo[]> {
  const { data } = await api.get<{ languages: BackendLanguage[] }>('/languages');
  return (data.languages ?? []).map(adaptLanguage);
}

/** Health check */
export async function healthCheck(): Promise<HealthResponse> {
  const { data } = await api.get<BackendHealthResponse>('/health');
  return {
    status: data.status,
    timestamp: new Date().toISOString(),
  };
}

/**
 * Open a WebSocket connection to stream job updates.
 * Falls back to polling if WebSocket is unavailable.
 */
export function subscribeToJob(
  jobId: string,
  onUpdate: (job: Job) => void,
  onError?: (error: Event) => void,
): () => void {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  // Backend route is /api/v1/submissions/:id/stream — proxied through nginx.
  const wsUrl = `${protocol}//${window.location.host}/api/v1/submissions/${jobId}/stream`;

  const ws = new WebSocket(wsUrl);

  ws.onmessage = (event) => {
    try {
      const backend = JSON.parse(event.data) as BackendJob;
      onUpdate(adaptJob(backend));
    } catch {
      console.error('Failed to parse WebSocket message:', event.data);
    }
  };

  ws.onerror = (event) => {
    console.error('WebSocket error:', event);
    onError?.(event);
  };

  ws.onclose = () => {
    console.debug('WebSocket closed for job:', jobId);
  };

  // Return cleanup function
  return () => {
    if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
      ws.close();
    }
  };
}

export default api;
