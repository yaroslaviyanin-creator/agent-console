import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './app/App'
import { restoreConnection } from './app/chatStore'
import { initializeTelegram } from './platform/telegram'
import './style.css'

void restoreConnection()
createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
void initializeTelegram()
