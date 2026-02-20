import { useState } from 'react';
import type { Job } from '../../types/submission';
import { isTerminalStatus } from '../../types/submission';
import { StatusBadge } from '../StatusBadge';

interface ResultPanelProps {
  job: Job | null;
  isLoading: boolean;
  error: string | null;
}

export default function ResultPanel({ job, isLoading, error }: ResultPanelProps) {
  if (error) {
    return (
      <div className="panel p-4">
        <div className="flex items-center gap-2 text-red-400 mb-2">
          <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L4.082 16.5c-.77.833.192 2.5 1.732 2.5z"
            />
          </svg>
          <span className="font-medium">Error</span>
        </div>
        <p className="text-sm text-dark-400">{error}</p>
      </div>
    );
  }

  if (!job && !isLoading) {
    return (
      <div className="panel p-8 flex flex-col items-center justify-center text-dark-500">
        <svg className="w-12 h-12 mb-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1.5}
            d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
          />
        </svg>
        <p className="text-sm">Run your code to see results here</p>
        <p className="text-xs text-dark-600 mt-1">Ctrl+Enter to submit</p>
      </div>
    );
  }

  if (isLoading && !job) {
    return (
      <div className="panel p-8 flex flex-col items-center justify-center">
        <div className="animate-spin w-8 h-8 border-2 border-sentinel-500 border-t-transparent rounded-full mb-3" />
        <p className="text-sm text-dark-400">Submitting...</p>
      </div>
    );
  }

  if (!job) return null;

  const isComplete = isTerminalStatus(job.status);

  return (
    <div className="panel overflow-hidden animate-fade-in">
      {/* Status bar */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-dark-800">
        <div className="flex items-center gap-3">
          <StatusBadge status={job.status} size="md" />
          {!isComplete && (
            <div className="flex items-center gap-1.5">
              <div className="animate-spin w-3.5 h-3.5 border-2 border-sentinel-500 border-t-transparent rounded-full" />
              <span className="text-xs text-dark-500">Processing...</span>
            </div>
          )}
        </div>

        {isComplete && (
          <div className="flex items-center gap-4 text-xs text-dark-500">
            {job.execution_time_ms !== null && (
              <span title="Execution time">⏱ {job.execution_time_ms}ms</span>
            )}
            {job.memory_used_bytes !== null && (
              <span title="Memory used">💾 {formatBytes(job.memory_used_bytes)}</span>
            )}
            {job.exit_code !== null && (
              <span title="Exit code">Exit: {job.exit_code}</span>
            )}
          </div>
        )}
      </div>

      {/* Output */}
      <div className="divide-y divide-dark-800">
        {/* stdout */}
        {job.stdout && (
          <OutputBlock label="stdout" content={job.stdout} colorClass="text-green-400" />
        )}

        {/* stderr */}
        {job.stderr && (
          <OutputBlock label="stderr" content={job.stderr} colorClass="text-red-400" />
        )}

        {/* No output */}
        {isComplete && !job.stdout && !job.stderr && (
          <div className="p-4 text-center text-sm text-dark-500">
            No output produced
          </div>
        )}
      </div>
    </div>
  );
}

/** Reusable output block with copy button. */
function OutputBlock({
  label,
  content,
  colorClass,
}: {
  label: string;
  content: string;
  colorClass: string;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard API unavailable
    }
  };

  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-medium text-dark-500 uppercase tracking-wider">
          {label}
        </span>
        <button
          onClick={handleCopy}
          className="text-xs text-dark-600 hover:text-dark-300 transition-colors flex items-center gap-1"
          title="Copy to clipboard"
        >
          {copied ? (
            <>
              <svg className="w-3.5 h-3.5 text-green-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" />
              </svg>
              Copied
            </>
          ) : (
            <>
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                />
              </svg>
              Copy
            </>
          )}
        </button>
      </div>
      <pre
        className={`text-sm font-mono ${colorClass} bg-dark-950 rounded-lg p-3 overflow-x-auto whitespace-pre-wrap break-words max-h-64 overflow-y-auto`}
      >
        {content}
      </pre>
    </div>
  );
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)}MB`;
}
