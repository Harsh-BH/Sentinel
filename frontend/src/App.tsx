import { useState, useCallback, useEffect } from 'react';
import toast from 'react-hot-toast';
import { Header } from './components/Header';
import { CodeEditor, getDefaultCode } from './components/CodeEditor';
import { LanguageSelector } from './components/LanguageSelector';
import { StdinPanel } from './components/StdinPanel';
import { ResultPanel } from './components/ResultPanel';
import { SubmissionHistory } from './components/SubmissionHistory';
import { useJobTracking } from './hooks/useJobTracking';
import { useSubmissionHistory } from './hooks/useSubmissionHistory';
import { submitCode, getJob } from './services/api';
import type { Language } from './types/submission';

export default function App() {
  const [language, setLanguage] = useState<Language>('python');
  const [code, setCode] = useState(getDefaultCode('python'));
  const [stdin, setStdin] = useState('');
  const [jobId, setJobId] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [showHistory, setShowHistory] = useState(false);

  const { job, isLoading, error, isComplete } = useJobTracking(jobId);
  const { entries, addEntry, updateStatus, clearHistory } = useSubmissionHistory();

  // Keep history status in sync with live updates.
  useEffect(() => {
    if (job && jobId) {
      updateStatus(jobId, job.status);
    }
  }, [job, jobId, updateStatus]);

  const handleLanguageChange = useCallback(
    (newLang: Language) => {
      setLanguage(newLang);
      setCode(getDefaultCode(newLang));
    },
    [],
  );

  const handleSubmit = useCallback(async () => {
    if (!code.trim()) {
      toast.error('Please write some code first');
      return;
    }

    setIsSubmitting(true);
    setJobId(null);

    try {
      const response = await submitCode({
        source_code: code,
        language,
        stdin: stdin || undefined,
      });

      setJobId(response.id);

      // Record in history.
      addEntry({
        id: response.id,
        language,
        status: response.status,
        codePreview: code.slice(0, 60).replace(/\n/g, ' '),
      });

      toast.success('Code submitted!');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Submission failed');
    } finally {
      setIsSubmitting(false);
    }
  }, [code, language, stdin, addEntry]);

  /** Select a past submission from history. */
  const handleHistorySelect = useCallback(
    async (id: string) => {
      setJobId(id);
      try {
        const pastJob = await getJob(id);
        if (pastJob.language) {
          setLanguage(pastJob.language);
          setCode(pastJob.source_code);
          setStdin(pastJob.stdin ?? '');
        }
      } catch {
        // Ignore — the job tracking hook will pick it up.
      }
    },
    [],
  );

  const handleReset = useCallback(() => {
    setJobId(null);
    setCode(getDefaultCode(language));
    setStdin('');
  }, [language]);

  // Ctrl+Enter / Cmd+Enter shortcut to submit.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        handleSubmit();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [handleSubmit]);

  const isRunning = isSubmitting || (isLoading && !isComplete);

  return (
    <div className="h-screen flex flex-col overflow-hidden">
      <Header onReset={handleReset} />

      <main className="flex-1 flex flex-col md:flex-row overflow-hidden">
        {/* History sidebar — hidden on mobile, toggled */}
        <div
          className={`
            ${showHistory ? 'flex' : 'hidden'} md:flex
            w-full md:w-56 lg:w-64 flex-shrink-0 flex-col
            border-b md:border-b-0 md:border-r border-dark-800
            bg-dark-900/50 max-h-48 md:max-h-full overflow-hidden
          `}
        >
          <SubmissionHistory
            entries={entries}
            activeId={jobId}
            onSelect={handleHistorySelect}
            onClear={clearHistory}
          />
        </div>

        {/* Center panel — Editor */}
        <div className="flex-1 flex flex-col min-w-0 border-r border-dark-800">
          {/* Toolbar */}
          <div className="flex items-center justify-between px-3 sm:px-4 py-2 sm:py-3 border-b border-dark-800 bg-dark-900/30 gap-2">
            <div className="flex items-center gap-2">
              {/* History toggle (mobile) */}
              <button
                onClick={() => setShowHistory(!showHistory)}
                className="md:hidden btn-secondary p-2"
                title="Toggle history"
              >
                <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
                </svg>
              </button>
              <LanguageSelector
                value={language}
                onChange={handleLanguageChange}
                disabled={isRunning}
              />
            </div>

            <button
              onClick={handleSubmit}
              disabled={isRunning || !code.trim()}
              className="btn-primary flex items-center gap-2 text-sm whitespace-nowrap"
            >
              {isRunning ? (
                <>
                  <div className="animate-spin w-4 h-4 border-2 border-white border-t-transparent rounded-full" />
                  <span className="hidden sm:inline">Running...</span>
                </>
              ) : (
                <>
                  <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"
                    />
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                    />
                  </svg>
                  <span className="hidden sm:inline">Run Code</span>
                  <kbd className="hidden lg:inline-flex items-center gap-0.5 px-1.5 py-0.5 text-[10px] font-mono bg-dark-800 rounded text-dark-400">
                    Ctrl+↵
                  </kbd>
                </>
              )}
            </button>
          </div>

          {/* Code editor */}
          <div className="flex-1 min-h-0">
            <CodeEditor
              language={language}
              value={code}
              onChange={setCode}
              readOnly={isRunning}
            />
          </div>

          {/* Stdin */}
          <div className="border-t border-dark-800">
            <StdinPanel value={stdin} onChange={setStdin} disabled={isRunning} />
          </div>
        </div>

        {/* Right panel — Results */}
        <div className="w-full md:w-[420px] lg:w-[480px] flex-shrink-0 flex flex-col overflow-y-auto p-3 sm:p-4 gap-3 bg-dark-950">
          <h2 className="text-sm font-medium text-dark-500 uppercase tracking-wider">
            Output
          </h2>
          <ResultPanel job={job} isLoading={isLoading || isSubmitting} error={error} />
        </div>
      </main>

      {/* Status bar */}
      <footer className="flex items-center justify-between px-4 py-1.5 text-xs text-dark-600 border-t border-dark-800 bg-dark-900/50">
        <span>
          {language === 'python' ? 'Python 3.12' : 'C++ 17 (g++ 13)'}
          {job && ` • Job: ${job.id.slice(0, 8)}...`}
        </span>
        <div className="flex items-center gap-3">
          <span>{entries.length} submissions</span>
          <span className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-full bg-green-500" />
            Connected
          </span>
        </div>
      </footer>
    </div>
  );
}
