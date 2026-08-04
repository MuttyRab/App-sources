import React from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import { LocalIpaSigner } from '@/components/local-ipa-signer'

const root = document.querySelector<HTMLDivElement>('#app')
if (!root) throw new Error('Missing app root')

createRoot(root).render(
  <React.StrictMode>
    <LocalIpaSigner />
  </React.StrictMode>,
)

if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {
      // Signing still works without offline shell caching.
    })
  })
}
