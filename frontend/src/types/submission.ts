/** Execution job status - mirrors backend enum */
export type ExecutionStatus =
  | 'QUEUED'
  | 'PROCESSING'
  | 'RUNNING'
  | 'SUCCESS'
  | 'COMPILE_ERROR'
  | 'RUNTIME_ERROR'
  | 'TIMEOUT'
  | 'MEMORY_LIMIT'
  | 'SYSTEM_ERROR';

/** Supported programming languages */
export type Language = 'python' | 'cpp';

/** Submission request payload */
export interface SubmitRequest {
  source_code: string;
  language: Language;
  stdin?: string;
}

/** Job response from API */
export interface Job {
  id: string;
  source_code: string;
  language: Language;
  stdin: string;
  status: ExecutionStatus;
  stdout: string | null;
  stderr: string | null;
  exit_code: number | null;
  execution_time_ms: number | null;
  memory_used_bytes: number | null;
  created_at: string;
  updated_at: string;
}

/** Submission response (202 Accepted) */
export interface SubmitResponse {
  id: string;
  status: ExecutionStatus;
  created_at: string;
}

/** Language info from /api/v1/languages */
export interface LanguageInfo {
  id: Language;
  name: string;
  version: string;
  file_extension: string;
  monaco_id: string;
}

/** Health check response */
export interface HealthResponse {
  status: string;
  timestamp: string;
}

/** WebSocket message for real-time updates */
export interface WSMessage {
  type: 'status_update' | 'result';
  data: Job;
}

/** Terminal status helpers */
export const TERMINAL_STATUSES: ExecutionStatus[] = [
  'SUCCESS',
  'COMPILE_ERROR',
  'RUNTIME_ERROR',
  'TIMEOUT',
  'MEMORY_LIMIT',
  'SYSTEM_ERROR',
];

export const isTerminalStatus = (status: ExecutionStatus): boolean =>
  TERMINAL_STATUSES.includes(status);

export const STATUS_COLORS: Record<ExecutionStatus, string> = {
  QUEUED: 'bg-dark-600 text-dark-200',
  PROCESSING: 'bg-yellow-600/20 text-yellow-400',
  RUNNING: 'bg-blue-600/20 text-blue-400',
  SUCCESS: 'bg-green-600/20 text-green-400',
  COMPILE_ERROR: 'bg-red-600/20 text-red-400',
  RUNTIME_ERROR: 'bg-red-600/20 text-red-400',
  TIMEOUT: 'bg-orange-600/20 text-orange-400',
  MEMORY_LIMIT: 'bg-orange-600/20 text-orange-400',
  SYSTEM_ERROR: 'bg-red-600/20 text-red-400',
};

export const STATUS_LABELS: Record<ExecutionStatus, string> = {
  QUEUED: 'Queued',
  PROCESSING: 'Processing',
  RUNNING: 'Running',
  SUCCESS: 'Success',
  COMPILE_ERROR: 'Compile Error',
  RUNTIME_ERROR: 'Runtime Error',
  TIMEOUT: 'Timeout',
  MEMORY_LIMIT: 'Memory Limit',
  SYSTEM_ERROR: 'System Error',
};
