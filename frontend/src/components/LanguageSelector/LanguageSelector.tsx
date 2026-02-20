import type { Language } from '../../types/submission';

interface LanguageSelectorProps {
  value: Language;
  onChange: (language: Language) => void;
  disabled?: boolean;
}

const LANGUAGES: { id: Language; name: string; icon: string }[] = [
  { id: 'python', name: 'Python 3.12', icon: '🐍' },
  { id: 'cpp', name: 'C++ 17 (g++)', icon: '⚡' },
];

export default function LanguageSelector({
  value,
  onChange,
  disabled = false,
}: LanguageSelectorProps) {
  return (
    <div className="flex gap-2">
      {LANGUAGES.map((lang) => (
        <button
          key={lang.id}
          onClick={() => onChange(lang.id)}
          disabled={disabled}
          className={`
            flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium
            transition-all duration-200 border
            ${
              value === lang.id
                ? 'bg-sentinel-600/20 border-sentinel-500 text-sentinel-300'
                : 'bg-dark-800 border-dark-700 text-dark-400 hover:bg-dark-700 hover:text-dark-200'
            }
            disabled:opacity-50 disabled:cursor-not-allowed
          `}
        >
          <span>{lang.icon}</span>
          <span>{lang.name}</span>
        </button>
      ))}
    </div>
  );
}
