import { create } from 'zustand'

export type ThemePreference = 'dark' | 'light' | 'system'
export type AccentPreset = 'mint' | 'ocean' | 'violet' | 'amber' | 'rose' | 'graphite'

export interface AppearanceState {
  theme: ThemePreference
  accent: AccentPreset
  ambientGradient: boolean
  setTheme: (theme: ThemePreference) => void
  setAccent: (accent: AccentPreset) => void
  setAmbientGradient: (enabled: boolean) => void
}

export const ACCENT_PRESETS: readonly { id: AccentPreset; label: string; swatch: string }[] = [
  { id: 'mint', label: 'Изумрудный', swatch: '#8ee3bb' },
  { id: 'ocean', label: 'Синий', swatch: '#7ab3ff' },
  { id: 'violet', label: 'Фиолетовый', swatch: '#b794f4' },
  { id: 'amber', label: 'Янтарный', swatch: '#f0c975' },
  { id: 'rose', label: 'Розовый', swatch: '#f48fb1' },
  { id: 'graphite', label: 'Графит', swatch: '#94a3b8' },
]

const STORAGE_KEY = 'agent-console.appearance.v1'

interface StoredAppearance {
  theme?: ThemePreference
  accent?: AccentPreset
  ambientGradient?: boolean
}

function readStored(): { theme: ThemePreference; accent: AccentPreset; ambientGradient: boolean } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { theme: 'dark', accent: 'mint', ambientGradient: true }
    const parsed = JSON.parse(raw) as StoredAppearance
    const theme: ThemePreference = parsed.theme === 'light' || parsed.theme === 'system' || parsed.theme === 'dark' ? parsed.theme : 'dark'
    const accent: AccentPreset = ACCENT_PRESETS.some(p => p.id === parsed.accent) ? parsed.accent! : 'mint'
    const ambientGradient = typeof parsed.ambientGradient === 'boolean' ? parsed.ambientGradient : true
    return { theme, accent, ambientGradient }
  } catch {
    return { theme: 'dark', accent: 'mint', ambientGradient: true }
  }
}

function saveStored(next: { theme: ThemePreference; accent: AccentPreset; ambientGradient: boolean }) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Ignore storage quota / privacy mode failures.
  }
}

function resolveTheme(theme: ThemePreference): 'dark' | 'light' {
  if (theme === 'light' || theme === 'dark') return theme
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches) {
    return 'light'
  }
  return 'dark'
}

export function applyAppearanceToDom(state: { theme: ThemePreference; accent: AccentPreset; ambientGradient: boolean }) {
  if (typeof document === 'undefined') return
  const resolved = resolveTheme(state.theme)
  const root = document.documentElement
  root.dataset.theme = resolved
  root.dataset.accent = state.accent
  root.dataset.ambient = state.ambientGradient ? 'on' : 'off'
  root.style.colorScheme = resolved
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (meta) meta.content = resolved === 'light' ? '#f4f5f7' : '#07080d'
}

const initial = readStored()

export const useAppearance = create<AppearanceState>((set, get) => ({
  ...initial,
  setTheme: theme => {
    const next = { theme, accent: get().accent, ambientGradient: get().ambientGradient }
    saveStored(next)
    applyAppearanceToDom(next)
    set({ theme })
  },
  setAccent: accent => {
    const next = { theme: get().theme, accent, ambientGradient: get().ambientGradient }
    saveStored(next)
    applyAppearanceToDom(next)
    set({ accent })
  },
  setAmbientGradient: ambientGradient => {
    const next = { theme: get().theme, accent: get().accent, ambientGradient }
    saveStored(next)
    applyAppearanceToDom(next)
    set({ ambientGradient })
  },
}))

let mediaListenerBound = false

export function initializeAppearance() {
  const current = useAppearance.getState()
  applyAppearanceToDom(current)
  if (mediaListenerBound || typeof window === 'undefined' || !window.matchMedia) return
  mediaListenerBound = true
  const query = window.matchMedia('(prefers-color-scheme: light)')
  query.addEventListener('change', () => {
    const latest = useAppearance.getState()
    if (latest.theme === 'system') applyAppearanceToDom(latest)
  })
}
