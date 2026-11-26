import { useEffect, useRef, useState } from 'react'
import { CodeXml } from 'lucide-react'
import { Login } from './Login'

export type AuthPhase = 'loading' | 'login' | 'authenticated'

const MIN_SPLASH_MS = 600
const EXIT_DURATION_MS = 320

export function AuthScene({
  authenticated,
  restoring,
  skipSplash = false,
  onExited,
}: {
  authenticated: boolean
  restoring: boolean
  skipSplash?: boolean
  onExited: () => void
}) {
  const [phase, setPhase] = useState<AuthPhase>(() =>
    authenticated ? 'authenticated' : skipSplash ? 'login' : 'loading',
  )
  const mountedAtRef = useRef<number>(Date.now())
  const exitCalledRef = useRef(false)

  useEffect(() => {
    if (authenticated) {
      setPhase('authenticated')
      return
    }
    if (skipSplash) {
      setPhase('login')
      return
    }
    if (restoring) {
      setPhase('loading')
      return
    }
    const elapsed = Date.now() - mountedAtRef.current
    const remaining = Math.max(0, MIN_SPLASH_MS - elapsed)
    if (remaining === 0) {
      setPhase('login')
      return
    }
    const timer = window.setTimeout(() => {
      setPhase(prev => (prev === 'authenticated' ? prev : 'login'))
    }, remaining)
    return () => window.clearTimeout(timer)
  }, [authenticated, restoring, skipSplash])

  useEffect(() => {
    if (phase !== 'authenticated' || exitCalledRef.current) return
    const timer = window.setTimeout(() => {
      if (!exitCalledRef.current) {
        exitCalledRef.current = true
        onExited()
      }
    }, EXIT_DURATION_MS)
    return () => window.clearTimeout(timer)
  }, [phase, onExited])

  return (
    <main className="auth-scene" data-phase={phase} aria-busy={phase === 'loading'}>
      <div className="titlebar-drag-strip" data-tauri-drag-region aria-hidden="true" />
      <div className="auth-stage">
        <div className="auth-hero">
          <div className="auth-squircle" aria-hidden="true">
            <CodeXml size={30} strokeWidth={2.1} />
          </div>
          <span className="auth-app-name">Agent Console</span>
          <span className="auth-loading-hint" role="status" aria-live="polite">
            {phase === 'loading' ? (restoring ? 'Проверка сессии…' : 'Инициализация…') : ''}
          </span>
        </div>
        <div className="auth-card-slot" aria-hidden={phase !== 'login'}>
          <div className="auth-card-slot-inner">
            <Login />
          </div>
        </div>
      </div>
    </main>
  )
}
