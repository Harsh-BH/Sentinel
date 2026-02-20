import type { ExecutionStatus } from '../../types/submission';
import { STATUS_COLORS, STATUS_LABELS } from '../../types/submission';

interface StatusBadgeProps {
  status: ExecutionStatus;
  /** Show a pulsing dot animation for non-terminal statuses */
  animated?: boolean;
  size?: 'sm' | 'md';
}

/**
 * Color-coded status badge for execution jobs.
 * - Green: Success
 * - Red: Errors (compile, runtime, system)
 * - Orange: Timeout, Memory limit
 * - Yellow: Processing
 * - Blue: Running
 * - Gray: Queued
 */
export default function StatusBadge({ status, animated = true, size = 'sm' }: StatusBadgeProps) {
  const isActive = animated && !isTerminal(status);

  const sizeClasses = size === 'sm' ? 'px-2 py-0.5 text-xs' : 'px-2.5 py-1 text-sm';

  return (
    <span
      className={`
        inline-flex items-center gap-1.5 rounded-full font-medium uppercase tracking-wider
        ${sizeClasses}
        ${STATUS_COLORS[status]}
      `}
    >
      {isActive && (
        <span className="relative flex h-2 w-2">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 bg-current" />
          <span className="relative inline-flex rounded-full h-2 w-2 bg-current" />
        </span>
      )}
      {STATUS_LABELS[status]}
    </span>
  );
}

function isTerminal(status: ExecutionStatus): boolean {
  return ['SUCCESS', 'COMPILE_ERROR', 'RUNTIME_ERROR', 'TIMEOUT', 'MEMORY_LIMIT', 'SYSTEM_ERROR'].includes(status);
}
