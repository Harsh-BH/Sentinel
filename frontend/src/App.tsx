import { useState, useCallback } from 'react';
import toast from 'react-hot-toast';
import { Header } from './components/Header';
import { CodeEditor, getDefaultCode } from './components/CodeEditor';
import { LanguageSelector } from './components/LanguageSelector';
import { StdinPanel } from './components/StdinPanel';
import { ResultPanel } from './components/ResultPanel';
import { useJobTracking } from './hooks/useJobTracking';
import { submitCode } from './services/api';
import type { Language } from './types/submission';

export default function App() {
  const [language, setLanguage] = useState<Language>('python');
  const [code, setCode] = useState(getDefaultCode('python'));
  const [stdin, setStdin] = useState('');
  const [jobId, setJobId] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  const { job, isLoading, error, isComplete } = useJobTracking(jobId);

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
      toast.success('Code submitted successfully!');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Submission failed');
    } finally {
      setIsSubmitting(false);
    }
  }, [code, language, stdin]);

  const handleReset = useCallback(() => {
    setJobId(null);
    setCode(getDefaultCode(language));
    setStdin('');
  }, [language]);

  const isRunning = isSubmitting || (isLoading && !isComplete);

  return (
    <div className="h-screen flex flex-col overflow-hidden">
      <Header onReset={handleReset} />

      <main className="flex-1 flex overflow-hidden">
        {/* Left panel — Editor */}
        <div className="flex-1 flex flex-col min-w-0 border-r border-dark-800">
          {/* Toolbar */}
          <div className="flex items-center justify-between px-4 py-3 border-b border-dark-800 bg-dark-900/30">
            <LanguageSelector
              value={language}
              onChange={handleLanguageChange}
              disabled={isRunning}
            />

            <button
              onClick={handleSubmit}
              disabled={isRunning || !code.trim()}
              className="btn-primary flex items-center gap-2"
            >
              {isRunning ? (
                <>
                  <div className="animate-spin w-4 h-4 border-2 border-white border-t-transparent rounded-full" />
                  Running...
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
                  Run Code
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
        <div className="w-[480px] flex-shrink-0 flex flex-col overflow-y-auto p-4 gap-4 bg-dark-950">
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
        <span className="flex items-center gap-2">
          <span className="w-2 h-2 rounded-full bg-green-500" />
          Connected
        </span>
      </footer>
    </div>
  );
}
