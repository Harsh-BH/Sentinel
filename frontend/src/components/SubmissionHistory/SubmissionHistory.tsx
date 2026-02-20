import { StatusBadge } from '../StatusBadge';
import type { HistoryEntry } from '../../hooks/useSubmissionHistory';
import type { Language } from '../../types/submission';

interface SubmissionHistoryProps {
  entries: HistoryEntry[];
  activeId: string | null;
  onSelect: (id: string) => void;
  onClear: () => void;
}

const LANG_ICON: Record<Language, string> = {
  python: '🐍',
  cpp: '⚡',
};

export default function SubmissionHistory({
  entries,
  activeId,
  onSelect,
  onClear,
}: SubmissionHistoryProps) {
  if (entries.length === 0) {
    return (
      <div className="p-4 text-center text-dark-500 text-sm">
        <svg
          className="w-8 h-8 mx-auto mb-2 opacity-40"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1.5}
            d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
        No submissions yet
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-dark-800">
        <span className="text-xs font-medium text-dark-500 uppercase tracking-wider">
          History ({entries.length})
        </span>
        <button
          onClick={onClear}
          className="text-xs text-dark-600 hover:text-red-400 transition-colors"
          title="Clear history"
        >
          Clear
        </button>
      </div>

      {/* List */}
      <div className="flex-1 overflow-y-auto">
        {entries.map((entry) => (
          <button
            key={entry.id}
            onClick={() => onSelect(entry.id)}
            className={`
              w-full text-left px-4 py-3 border-b border-dark-800/50
              transition-colors duration-150 group
              ${
                entry.id === activeId
                  ? 'bg-sentinel-600/10 border-l-2 border-l-sentinel-500'
                  : 'hover:bg-dark-800/50 border-l-2 border-l-transparent'
              }
            `}
          >
            <div className="flex items-center justify-between mb-1.5">
              <div className="flex items-center gap-2 min-w-0">
                <span className="text-sm" title={entry.language}>
                  {LANG_ICON[entry.language]}
                </span>
                <span className="text-xs font-mono text-dark-400 truncate">
                  {entry.id.slice(0, 8)}
                </span>
              </div>
              <StatusBadge status={entry.status} animated={false} size="sm" />
            </div>

            <p className="text-xs text-dark-500 font-mono truncate leading-relaxed">
              {entry.codePreview}
            </p>

            <span className="text-[10px] text-dark-600 mt-1 block">
              {timeAgo(entry.createdAt)}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

/** Simple relative-time formatter. */
function timeAgo(iso: string): string {
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 10) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
