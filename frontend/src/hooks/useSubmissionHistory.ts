import { useCallback, useEffect, useState } from 'react';
import type { ExecutionStatus, Language } from '../types/submission';

const STORAGE_KEY = 'sentinel:submission_history';
const MAX_HISTORY = 50;

/** Lightweight record stored in localStorage */
export interface HistoryEntry {
  id: string;
  language: Language;
  status: ExecutionStatus;
  createdAt: string;
  /** First 60 chars of source for preview */
  codePreview: string;
}

/**
 * Manages a persisted list of recent submissions.
 * Uses localStorage so history survives page reloads.
 */
export function useSubmissionHistory() {
  const [entries, setEntries] = useState<HistoryEntry[]>(() => load());

  // Persist whenever entries change.
  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
    } catch {
      // localStorage full — silently drop
    }
  }, [entries]);

  /** Add a new submission to history (most-recent first). */
  const addEntry = useCallback(
    (entry: Omit<HistoryEntry, 'createdAt'> & { createdAt?: string }) => {
      setEntries((prev) => {
        // Deduplicate by id.
        const filtered = prev.filter((e) => e.id !== entry.id);
        const newEntry: HistoryEntry = {
          ...entry,
          createdAt: entry.createdAt ?? new Date().toISOString(),
        };
        return [newEntry, ...filtered].slice(0, MAX_HISTORY);
      });
    },
    [],
  );

  /** Update the status of an existing entry (called as WS updates arrive). */
  const updateStatus = useCallback(
    (id: string, status: ExecutionStatus) => {
      setEntries((prev) =>
        prev.map((e) => (e.id === id ? { ...e, status } : e)),
      );
    },
    [],
  );

  /** Clear all history. */
  const clearHistory = useCallback(() => {
    setEntries([]);
  }, []);

  return { entries, addEntry, updateStatus, clearHistory };
}

function load(): HistoryEntry[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    return JSON.parse(raw) as HistoryEntry[];
  } catch {
    return [];
  }
}
