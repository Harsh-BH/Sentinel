import axios from 'axios';
import type { SubmitRequest, SubmitResponse, Job, LanguageInfo, HealthResponse } from '../types/submission';

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

/** Submit code for execution */
export async function submitCode(req: SubmitRequest): Promise<SubmitResponse> {
  const { data } = await api.post<SubmitResponse>('/submissions', req);
  return data;
}

/** Get job by ID (polling fallback) */
export async function getJob(id: string): Promise<Job> {
  const { data } = await api.get<Job>(`/submissions/${id}`);
  return data;
}

/** List supported languages */
export async function getLanguages(): Promise<LanguageInfo[]> {
  const { data } = await api.get<LanguageInfo[]>('/languages');
  return data;
}

/** Health check */
export async function healthCheck(): Promise<HealthResponse> {
  const { data } = await api.get<HealthResponse>('/health');
  return data;
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
  const wsUrl = `${protocol}//${window.location.host}/ws/submissions/${jobId}`;

  const ws = new WebSocket(wsUrl);

  ws.onmessage = (event) => {
    try {
      const job: Job = JSON.parse(event.data);
      onUpdate(job);
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
