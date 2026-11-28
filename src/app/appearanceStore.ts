import { create } from 'zustand'
import type { UISettings } from '../protocol/types'

export type ThemePreference = 'dark' | 'light' | 'system'
export type AccentPreset = 'mint' | 'ocean' | 'violet' | 'amber' | 'rose' | 'graphite' | 'custom'
export type GradientPalette = 'accent' | 'indigo' | 'ocean' | 'emerald' | 'sunset' | 'monochrome' | 'custom'

export interface AppearanceState {
  theme: ThemePreference
  accent: AccentPreset
  accentCustomColor: string
  ambientGradient: boolean
  gradientPalette: GradientPalette
  gradientCustomColor: string
  gradientCustomColor2: string
  setTheme: (theme: ThemePreference) => void
  setAccent: (accent: AccentPreset) => void
  setAccentCustomColor: (hex: string) => void
  setAmbientGradient: (enabled: boolean) => void
  setGradientPalette: (palette: GradientPalette) => void
  setGradientCustomColor: (hex: string) => void
  setGradientCustomColor2: (hex: string) => void
}

export const ACCENT_PRESETS: readonly { id: Exclude<AccentPreset, 'custom'>; label: string; swatch: string }[] = [
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
export const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/

interface StoredAppearance {
  theme?: ThemePreference
  accent?: AccentPreset
  accentCustomColor?: string
  ambientGradient?: boolean
  gradientPalette?: GradientPalette
  gradientCustomColor?: string
  gradientCustomColor2?: string
}

type AppearanceSnapshot = {
  theme: ThemePreference
  accent: AccentPreset
  accentCustomColor: string
  ambientGradient: boolean
  gradientPalette: GradientPalette
  gradientCustomColor: string
  gradientCustomColor2: string
}

function normalizeHex(raw: unknown, fallback: string): string {
  if (typeof raw !== 'string') return fallback
  const trimmed = raw.trim()
  return HEX_COLOR_RE.test(trimmed) ? trimmed.toLowerCase() : fallback
}

function parseSnapshot(parsed: StoredAppearance | null | undefined, base?: AppearanceSnapshot): AppearanceSnapshot {
  const fallback: AppearanceSnapshot = base ?? {
    theme: 'dark',
    accent: 'mint',
    accentCustomColor: '#7c5cff',
    ambientGradient: true,
    gradientPalette: 'accent',
    gradientCustomColor: '#8b5cf6',
    gradientCustomColor2: '#3b82f6',
  }
  if (!parsed || typeof parsed !== 'object') return fallback
  const theme: ThemePreference = parsed.theme === 'light' || parsed.theme === 'system' || parsed.theme === 'dark' ? parsed.theme : fallback.theme
  const accent: AccentPreset =
    parsed.accent === 'custom' || ACCENT_PRESETS.some(p => p.id === parsed.accent)
      ? parsed.accent!
      : fallback.accent
  const accentCustomColor = normalizeHex(parsed.accentCustomColor, fallback.accentCustomColor)
  const ambientGradient = typeof parsed.ambientGradient === 'boolean' ? parsed.ambientGradient : fallback.ambientGradient
  const gradientPalette: GradientPalette =
    parsed.gradientPalette === 'custom' || GRADIENT_PALETTES.some(p => p.id === parsed.gradientPalette)
      ? parsed.gradientPalette!
      : fallback.gradientPalette
  const gradientCustomColor = normalizeHex(parsed.gradientCustomColor, fallback.gradientCustomColor)
  const gradientCustomColor2 = normalizeHex(parsed.gradientCustomColor2, fallback.gradientCustomColor2)
  return { theme, accent, accentCustomColor, ambientGradient, gradientPalette, gradientCustomColor, gradientCustomColor2 }
}

function readStored(): AppearanceSnapshot {
  const fallback = parseSnapshot(null)
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return fallback
    return parseSnapshot(JSON.parse(raw) as StoredAppearance, fallback)
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

function hexToRgb(hex: string, fallback = '#8b5cf6'): [number, number, number] {
  const clean = HEX_COLOR_RE.test(hex) ? hex : fallback
  return [
    parseInt(clean.slice(1, 3), 16),
    parseInt(clean.slice(3, 5), 16),
    parseInt(clean.slice(5, 7), 16),
  ]
}

export function resolveTheme(theme: ThemePreference): 'dark' | 'light' {
  if (theme === 'light' || theme === 'dark') return theme
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches) {
    return 'light'
  }
  return 'dark'
}

const ACCENT_CSS_PROPS = [
  '--accent',
  '--accent-solid',
  '--accent-soft',
  '--accent-border',
  '--accent-text',
  '--accent-on-solid',
] as const

const ORB_CSS_PROPS = [
  '--orb-1',
  '--orb-1-fade',
  '--orb-2',
  '--orb-2-fade',
  '--orb-3',
  '--orb-3-fade',
] as const

export function applyAppearanceToDom(state: AppearanceSnapshot) {
  if (typeof document === 'undefined') return
  const resolved = resolveTheme(state.theme)
  const root = document.documentElement
  root.dataset.theme = resolved
  root.dataset.accent = state.accent
  root.dataset.ambient = resolved === 'light' ? 'off' : state.ambientGradient ? 'on' : 'off'
  root.dataset.palette = state.gradientPalette
  root.style.colorScheme = resolved

  if (state.accent === 'custom') {
    const [r, g, b] = hexToRgb(state.accentCustomColor, '#7c5cff')
    const luma = 0.299 * r + 0.587 * g + 0.114 * b
    const textRgb: [number, number, number] = resolved === 'light' && luma > 145
      ? [Math.round(r * 0.62), Math.round(g * 0.62), Math.round(b * 0.62)]
      : [r, g, b]
    const solidRgb: [number, number, number] = resolved === 'light' && luma > 175
      ? [Math.round(r * 0.72), Math.round(g * 0.72), Math.round(b * 0.72)]
      : [r, g, b]
    const solidLuma = 0.299 * solidRgb[0] + 0.587 * solidRgb[1] + 0.114 * solidRgb[2]
    root.style.setProperty('--accent', `rgb(${r}, ${g}, ${b})`)
    root.style.setProperty('--accent-solid', `rgb(${solidRgb[0]}, ${solidRgb[1]}, ${solidRgb[2]})`)
    root.style.setProperty('--accent-soft', `rgba(${r}, ${g}, ${b}, ${resolved === 'light' ? 0.14 : 0.16})`)
    root.style.setProperty('--accent-border', `rgba(${r}, ${g}, ${b}, ${resolved === 'light' ? 0.36 : 0.42})`)
    root.style.setProperty('--accent-text', `rgb(${textRgb[0]}, ${textRgb[1]}, ${textRgb[2]})`)
    root.style.setProperty('--accent-on-solid', solidLuma > 155 ? '#07100b' : '#ffffff')
  } else {
    for (const prop of ACCENT_CSS_PROPS) root.style.removeProperty(prop)
  }

  // Light theme stays clean and flat without animated background orbs.
  if (resolved === 'dark' && state.gradientPalette === 'custom') {
    const [r1, g1, b1] = hexToRgb(state.gradientCustomColor, '#8b5cf6')
    const [r2, g2, b2] = hexToRgb(state.gradientCustomColor2, '#3b82f6')
    root.style.setProperty('--orb-1', `rgba(${r1}, ${g1}, ${b1}, 0.28)`)
    root.style.setProperty('--orb-1-fade', `rgba(${r1}, ${g1}, ${b1}, 0.06)`)
    root.style.setProperty('--orb-2', `rgba(${r2}, ${g2}, ${b2}, 0.22)`)
    root.style.setProperty('--orb-2-fade', `rgba(${r2}, ${g2}, ${b2}, 0.06)`)
    const r3 = Math.round((r1 + r2) / 2)
    const g3 = Math.round((g1 + g2) / 2)
    const b3 = Math.round((b1 + b2) / 2)
    root.style.setProperty('--orb-3', `rgba(${r3}, ${g3}, ${b3}, 0.18)`)
    root.style.setProperty('--orb-3-fade', `rgba(${r3}, ${g3}, ${b3}, 0.05)`)
  } else {
    for (const prop of ORB_CSS_PROPS) root.style.removeProperty(prop)
  }

  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (meta) meta.content = resolved === 'light' ? '#f4f5f7' : '#07080d'
}

const initial = readStored()

type AppearanceSyncHandler = ((uiSettings: UISettings) => Promise<void> | void) | null
let syncHandler: AppearanceSyncHandler = null
let syncTimer: ReturnType<typeof setTimeout> | null = null

export function setAppearanceSyncHandler(handler: AppearanceSyncHandler) {
  syncHandler = handler
  if (!handler && syncTimer) {
    clearTimeout(syncTimer)
    syncTimer = null
  }
}

export function serializeAppearanceSnapshot(s: AppearanceSnapshot): UISettings {
  return {
    theme: s.theme,
    accent: s.accent,
    accentCustomColor: s.accentCustomColor,
    ambientGradient: s.ambientGradient,
    gradientPalette: s.gradientPalette,
    gradientCustomColor: s.gradientCustomColor,
    gradientCustomColor2: s.gradientCustomColor2,
  }
}

function scheduleServerSync(next: AppearanceSnapshot) {
  if (!syncHandler) return
  if (syncTimer) clearTimeout(syncTimer)
  syncTimer = setTimeout(() => {
    syncTimer = null
    if (!syncHandler) return
    void Promise.resolve(syncHandler(serializeAppearanceSnapshot(next))).catch(() => {})
  }, 450)
}

export function applyServerUISettings(raw: UISettings | undefined | null) {
  if (!raw || typeof raw !== 'object' || Object.keys(raw).length === 0) return
  const current = useAppearance.getState()
  const base: AppearanceSnapshot = {
    theme: current.theme,
    accent: current.accent,
    accentCustomColor: current.accentCustomColor,
    ambientGradient: current.ambientGradient,
    gradientPalette: current.gradientPalette,
    gradientCustomColor: current.gradientCustomColor,
    gradientCustomColor2: current.gradientCustomColor2,
  }
  const merged = parseSnapshot(raw, base)
  saveStored(merged)
  applyAppearanceToDom(merged)
  useAppearance.setState(merged)
}

export const useAppearance = create<AppearanceState>((set, get) => {
  const snapshot = (patch: Partial<AppearanceSnapshot>): AppearanceSnapshot => ({
    theme: patch.theme ?? get().theme,
    accent: patch.accent ?? get().accent,
    accentCustomColor: patch.accentCustomColor ?? get().accentCustomColor,
    ambientGradient: patch.ambientGradient ?? get().ambientGradient,
    gradientPalette: patch.gradientPalette ?? get().gradientPalette,
    gradientCustomColor: patch.gradientCustomColor ?? get().gradientCustomColor,
    gradientCustomColor2: patch.gradientCustomColor2 ?? get().gradientCustomColor2,
  })
  const commit = (patch: Partial<AppearanceSnapshot>) => {
    const next = snapshot(patch)
    saveStored(next)
    applyAppearanceToDom(next)
    scheduleServerSync(next)
    set(patch)
  }
  return {
    ...initial,
    setTheme: theme => commit({ theme }),
    setAccent: accent => commit({ accent }),
    setAccentCustomColor: hex => {
      const accentCustomColor = normalizeHex(hex, get().accentCustomColor)
      commit({ accent: 'custom', accentCustomColor })
    },
    setAmbientGradient: ambientGradient => commit({ ambientGradient }),
    setGradientPalette: gradientPalette => commit({ gradientPalette }),
    setGradientCustomColor: hex => {
      const gradientCustomColor = normalizeHex(hex, get().gradientCustomColor)
      commit({ gradientPalette: 'custom', gradientCustomColor })
    },
    setGradientCustomColor2: hex => {
      const gradientCustomColor2 = normalizeHex(hex, get().gradientCustomColor2)
      commit({ gradientPalette: 'custom', gradientCustomColor2 })
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
