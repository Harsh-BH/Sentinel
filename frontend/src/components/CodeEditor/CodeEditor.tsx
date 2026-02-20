import { useRef, useCallback } from 'react';
import Editor, { type OnMount } from '@monaco-editor/react';
import type { Language } from '../../types/submission';

interface CodeEditorProps {
  language: Language;
  value: string;
  onChange: (value: string) => void;
  readOnly?: boolean;
}

const LANGUAGE_MAP: Record<Language, string> = {
  python: 'python',
  cpp: 'cpp',
};

const DEFAULT_CODE: Record<Language, string> = {
  python: `# Welcome to Sentinel
# Write your Python code here

def main():
    name = input("Enter your name: ")
    print(f"Hello, {name}! Welcome to Sentinel.")

if __name__ == "__main__":
    main()
`,
  cpp: `// Welcome to Sentinel
// Write your C++ code here

#include <iostream>
#include <string>

int main() {
    std::string name;
    std::cout << "Enter your name: ";
    std::getline(std::cin, name);
    std::cout << "Hello, " << name << "! Welcome to Sentinel." << std::endl;
    return 0;
}
`,
};

export function getDefaultCode(language: Language): string {
  return DEFAULT_CODE[language] || '';
}

export default function CodeEditor({
  language,
  value,
  onChange,
  readOnly = false,
}: CodeEditorProps) {
  const editorRef = useRef<Parameters<OnMount>[0] | null>(null);

  const handleMount: OnMount = useCallback((editor) => {
    editorRef.current = editor;
    editor.focus();
  }, []);

  return (
    <div className="h-full w-full overflow-hidden rounded-lg border border-dark-800">
      <Editor
        height="100%"
        language={LANGUAGE_MAP[language]}
        value={value}
        onChange={(v) => onChange(v ?? '')}
        onMount={handleMount}
        theme="vs-dark"
        options={{
          readOnly,
          fontSize: 14,
          fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
          fontLigatures: true,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          padding: { top: 16, bottom: 16 },
          lineNumbers: 'on',
          renderLineHighlight: 'line',
          automaticLayout: true,
          tabSize: 4,
          insertSpaces: true,
          wordWrap: 'on',
          bracketPairColorization: { enabled: true },
          cursorBlinking: 'smooth',
          cursorSmoothCaretAnimation: 'on',
          smoothScrolling: true,
          contextmenu: false,
          suggest: {
            showKeywords: true,
            showSnippets: true,
          },
        }}
      />
    </div>
  );
}
