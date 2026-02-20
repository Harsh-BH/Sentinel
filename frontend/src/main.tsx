import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { Toaster } from 'react-hot-toast'
import App from './App'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Toaster
      position="top-right"
      toastOptions={{
        duration: 4000,
        style: {
          background: '#212529',
          color: '#f1f3f5',
          border: '1px solid #343a40',
        },
        success: {
          iconTheme: {
            primary: '#5c7cfa',
            secondary: '#f1f3f5',
          },
        },
        error: {
          iconTheme: {
            primary: '#ff6b6b',
            secondary: '#f1f3f5',
          },
        },
      }}
    />
    <App />
  </StrictMode>,
)
