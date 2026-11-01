import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from '@/app/App'
import './index.css'

// Back from the back/forward cache: the live stream was closed on the way out, so start fresh.
addEventListener('pageshow', (e) => {
  if (e.persisted) location.reload()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
