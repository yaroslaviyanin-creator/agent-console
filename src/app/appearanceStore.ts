import { create } from 'zustand'

export type ThemePreference = 'dark' | 'light' | 'system'
export type AccentPreset = 'mint' | 'ocean' | 'violet' | 'amber' | 'rose' | 'graphite'
export type GradientPalette = 'accent' | 'indigo' | 'ocean' | 'emerald' | 'sunset' | 'monochrome' | 'custom'

export interface AppearanceState {
  theme: ThemePreference
  accent: AccentPreset
  ambientGradient: boolean
  gradientPalette: GradientPalette
  gradientCustomColor: string
  setTheme: (theme: ThemePreference) => void
  setAccent: (accent: AccentPreset) => void
  setAmbientGradient: (enabled: boolean) => void
  setGradientPalette: (palette: GradientPalette) => void
  setGradientCustomColor: (hex: string) => void
}

export const ACCENT_PRESETS: readonly { id: AccentPreset; label: string; swatch: string }[] = [
  { id: 'mint', label: 'Изумрудный', swatch: '#8ee3bb' },
  { id: 'ocean', label: 'Синий', swatch: '#7ab3ff' },
  { id: 'violet', label: 'Фиолетовый', swatch: '#b794f4' },
  { id: 'amber', label: 'Янтарный', swatch: '#f0c975' },
  { id: 'rose', label: 'Розовый', swatch: '#f48fb1' },
  { id: 'graphite', label: 'Графит', swatch: '#94a3b8' },
]

export const GRADIENT_PALETTES: readonly { id: Exclude<GradientPalette, 'custom'>; label: string; swatch: string }[] = [
  { id: 'accent', label: 'Как акцент', swatch: 'linear-gradient(135deg, var(--accent), var(--accent-solid))' },
  { id: 'indigo', label: 'Индиго', swatch: 'linear-gradient(135deg, #6366f1, #a855f7)' },
  { id: 'ocean', label: 'Океан', swatch: 'linear-gradient(135deg, #0ea5e9, #3b82f6)' },
  { id: 'emerald', label: 'Изумруд', swatch: 'linear-gradient(135deg, #10b981, #14b8a6)' },
  { id: 'sunset', label: 'Закат', swatch: 'linear-gradient(135deg, #f97316, #ec4899)' },
  { id: 'monochrome', label: 'Монохром', swatch: 'linear-gradient(135deg, #94a3b8, #475569)' },
]

const STORAGE_KEY = 'agent-console.appearance.v1'
const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/

interface StoredAppearance {
  theme?: ThemePreference
  accent?: AccentPreset
  ambientGradient?: boolean
  gradientPalette?: GradientPalette
  gradientCustomColor?: string
}

type AppearanceSnapshot = {
  theme: ThemePreference
  accent: AccentPreset
  ambientGradient: boolean
  gradientPalette: GradientPalette
  gradientCustomColor: string
}

function readStored(): AppearanceSnapshot {
  const fallback: AppearanceSnapshot = {
    theme: 'dark',
    accent: 'mint',
    ambientGradient: true,
    gradientPalette: 'accent',
    gradientCustomColor: '#8b5cf6',
  }
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as StoredAppearance
    const theme: ThemePreference = parsed.theme === 'light' || parsed.theme === 'system' || parsed.theme === 'dark' ? parsed.theme : 'dark'
    const accent: AccentPreset = ACCENT_PRESETS.some(p => p.id === parsed.accent) ? parsed.accent! : 'mint'
    const ambientGradient = typeof parsed.ambientGradient === 'boolean' ? parsed.ambientGradient : true
    const gradientPalette: GradientPalette =
      parsed.gradientPalette === 'custom' || GRADIENT_PALETTES.some(p => p.id === parsed.gradientPalette)
        ? parsed.gradientPalette!
        : 'accent'
    const gradientCustomColor = typeof parsed.gradientCustomColor === 'string' && HEX_COLOR_RE.test(parsed.gradientCustomColor)
      ? parsed.gradientCustomColor
      : '#8b5cf6'
    return { theme, accent, ambientGradient, gradientPalette, gradientCustomColor }
  } catch {
    return fallback
  }
}

function saveStored(next: AppearanceSnapshot) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Ignore storage quota / privacy mode failures.
  }
}

function hexToRgb(hex: string): [number, number, number] {
  const clean = HEX_COLOR_RE.test(hex) ? hex : '#8b5cf6'
  return [
    parseInt(clean.slice(1, 3), 16),
    parseInt(clean.slice(3, 5), 16),
    parseInt(clean.slice(5, 7), 16),
  ]
}

function resolveTheme(theme: ThemePreference): 'dark' | 'light' {
  if (theme === 'light' || theme === 'dark') return theme
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches) {
    return 'light'
  }
  return 'dark'
}

export function applyAppearanceToDom(state: AppearanceSnapshot) {
  if (typeof document === 'undefined') return
  const resolved = resolveTheme(state.theme)
  const root = document.documentElement
  root.dataset.theme = resolved
  root.dataset.accent = state.accent
  root.dataset.ambient = state.ambientGradient ? 'on' : 'off'
  root.dataset.palette = state.gradientPalette
  root.style.colorScheme = resolved

  if (state.gradientPalette === 'custom') {
    const [r, g, b] = hexToRgb(state.gradientCustomColor)
    const a1 = resolved === 'light' ? 0.30 : 0.28
    const a2 = resolved === 'light' ? 0.22 : 0.20
    const af = resolved === 'light' ? 0.08 : 0.06
    root.style.setProperty('--orb-1', `rgba(${r}, ${g}, ${b}, ${a1})`)
    root.style.setProperty('--orb-1-fade', `rgba(${r}, ${g}, ${b}, ${af})`)
    root.style.setProperty('--orb-2', `rgba(${Math.max(0, r - 24)}, ${Math.min(255, g + 16)}, ${Math.min(255, b + 24)}, ${a2})`)
    root.style.setProperty('--orb-2-fade', `rgba(${r}, ${g}, ${b}, ${af})`)
    root.style.setProperty('--orb-3', `rgba(${Math.min(255, r + 20)}, ${Math.max(0, g - 16)}, ${b}, ${a2})`)
    root.style.setProperty('--orb-3-fade', `rgba(${r}, ${g}, ${b}, ${af})`)
  } else {
    root.style.removeProperty('--orb-1')
    root.style.removeProperty('--orb-1-fade')
    root.style.removeProperty('--orb-2')
    root.style.removeProperty('--orb-2-fade')
    root.style.removeProperty('--orb-3')
    root.style.removeProperty('--orb-3-fade')
  }

  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (meta) meta.content = resolved === 'light' ? '#f4f5f7' : '#07080d'
}

const initial = readStored()

export const useAppearance = create<AppearanceState>((set, get) => {
  const snapshot = (patch: Partial<AppearanceSnapshot>): AppearanceSnapshot => ({
    theme: patch.theme ?? get().theme,
    accent: patch.accent ?? get().accent,
    ambientGradient: patch.ambientGradient ?? get().ambientGradient,
    gradientPalette: patch.gradientPalette ?? get().gradientPalette,
    gradientCustomColor: patch.gradientCustomColor ?? get().gradientCustomColor,
  })
  return {
    ...initial,
    setTheme: theme => {
      const next = snapshot({ theme })
      saveStored(next)
      applyAppearanceToDom(next)
      set({ theme })
    },
    setAccent: accent => {
      const next = snapshot({ accent })
      saveStored(next)
      applyAppearanceToDom(next)
      set({ accent })
    },
    setAmbientGradient: ambientGradient => {
      const next = snapshot({ ambientGradient })
      saveStored(next)
      applyAppearanceToDom(next)
      set({ ambientGradient })
    },
    setGradientPalette: gradientPalette => {
      const next = snapshot({ gradientPalette })
      saveStored(next)
      applyAppearanceToDom(next)
      set({ gradientPalette })
    },
    setGradientCustomColor: gradientCustomColor => {
      const next = snapshot({ gradientPalette: 'custom', gradientCustomColor })
      saveStored(next)
      applyAppearanceToDom(next)
      set({ gradientPalette: 'custom', gradientCustomColor })
    },
  }
})

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
