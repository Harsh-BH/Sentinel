import { useState } from 'react';

interface StdinPanelProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}

export default function StdinPanel({ value, onChange, disabled = false }: StdinPanelProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  return (
    <div className="panel">
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center justify-between px-4 py-2.5 text-sm font-medium text-dark-300 hover:text-dark-100 transition-colors"
      >
        <span className="flex items-center gap-2">
          <span className="text-dark-500">{'>'}_</span>
          Standard Input (stdin)
          {value.trim() && (
            <span className="px-1.5 py-0.5 bg-sentinel-600/20 text-sentinel-400 rounded text-xs">
              has input
            </span>
          )}
        </span>
        <svg
          className={`w-4 h-4 transition-transform duration-200 ${isExpanded ? 'rotate-180' : ''}`}
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
        </svg>
      </button>

      {isExpanded && (
        <div className="px-4 pb-4 animate-fade-in">
          <textarea
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={disabled}
            placeholder="Enter input that will be passed to your program via stdin..."
            className="w-full h-24 bg-dark-950 border border-dark-800 rounded-lg px-3 py-2
                       text-sm font-mono text-dark-200 placeholder-dark-600
                       focus:outline-none focus:ring-1 focus:ring-sentinel-500 focus:border-sentinel-500
                       resize-y disabled:opacity-50"
          />
        </div>
      )}
    </div>
  );
}
