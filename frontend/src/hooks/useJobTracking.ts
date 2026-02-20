import { useCallback, useEffect, useRef, useState } from 'react';
import type { Job, ExecutionStatus } from '../types/submission';
import { getJob, subscribeToJob } from '../services/api';
import { isTerminalStatus } from '../types/submission';

interface UseJobPollingOptions {
  /** Polling interval in ms (used as WebSocket fallback) */
  pollInterval?: number;
  /** Use WebSocket for real-time updates */
  useWebSocket?: boolean;
}

interface UseJobPollingResult {
  job: Job | null;
  status: ExecutionStatus | null;
  isLoading: boolean;
  error: string | null;
  isComplete: boolean;
}

/**
 * Hook to track job execution status.
 * Uses WebSocket by default, falls back to polling on failure.
 */
export function useJobTracking(
  jobId: string | null,
  options: UseJobPollingOptions = {},
): UseJobPollingResult {
  const { pollInterval = 1000, useWebSocket = true } = options;

  const [job, setJob] = useState<Job | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const cleanupRef = useRef<(() => void) | null>(null);
  const pollTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const isComplete = job ? isTerminalStatus(job.status) : false;

  // Cleanup function
  const cleanup = useCallback(() => {
    if (cleanupRef.current) {
      cleanupRef.current();
      cleanupRef.current = null;
    }
    if (pollTimerRef.current) {
      clearInterval(pollTimerRef.current);
      pollTimerRef.current = null;
    }
  }, []);

  // Polling fallback
  const startPolling = useCallback(
    (id: string) => {
      const poll = async () => {
        try {
          const result = await getJob(id);
          setJob(result);
          if (isTerminalStatus(result.status)) {
            cleanup();
          }
        } catch (err) {
          setError(err instanceof Error ? err.message : 'Failed to fetch job');
          cleanup();
        }
      };

      poll(); // Initial fetch
      pollTimerRef.current = setInterval(poll, pollInterval);
    },
    [pollInterval, cleanup],
  );

  useEffect(() => {
    if (!jobId) {
      setJob(null);
      setIsLoading(false);
      setError(null);
      return;
    }

    setIsLoading(true);
    setError(null);

    if (useWebSocket) {
      // Try WebSocket first
      const unsubscribe = subscribeToJob(
        jobId,
        (updatedJob) => {
          setJob(updatedJob);
          setIsLoading(false);
          if (isTerminalStatus(updatedJob.status)) {
            cleanup();
          }
        },
        () => {
          // WebSocket failed, fall back to polling
          console.warn('WebSocket failed, falling back to polling');
          startPolling(jobId);
        },
      );
      cleanupRef.current = unsubscribe;

      // Also do an initial fetch so we have data while WS connects
      getJob(jobId)
        .then((result) => {
          setJob(result);
          setIsLoading(false);
        })
        .catch(() => {
          // Ignore — WS will provide updates
        });
    } else {
      startPolling(jobId);
      setIsLoading(false);
    }

    return cleanup;
  }, [jobId, useWebSocket, startPolling, cleanup]);

  return {
    job,
    status: job?.status ?? null,
    isLoading,
    error,
    isComplete,
  };
}
