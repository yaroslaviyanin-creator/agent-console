import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './app/App'
import { initializeAppearance } from './app/appearanceStore'
import { restoreConnection } from './app/chatStore'
import { initializePlatform } from './platform/tauri'
import { initializeTelegram } from './platform/telegram'
import './style.css'

initializeAppearance()
initializePlatform()
void restoreConnection()
createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
void initializeTelegram()
