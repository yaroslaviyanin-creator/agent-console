const STORAGE_KEY = 'agent-console.context-width.v1'
export const DEFAULT_CONTEXT_WIDTH = 280
export const MIN_CONTEXT_WIDTH = 240
export const MAX_CONTEXT_WIDTH = 460

export function clampContextWidth(width: number): number {
  if (!Number.isFinite(width)) return DEFAULT_CONTEXT_WIDTH
  return Math.max(MIN_CONTEXT_WIDTH, Math.min(MAX_CONTEXT_WIDTH, Math.round(width)))
}

export function readContextWidth(): number {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_CONTEXT_WIDTH
    return clampContextWidth(Number(raw))
  } catch {
    return DEFAULT_CONTEXT_WIDTH
  }
}

export function saveContextWidth(width: number): number {
  const clamped = clampContextWidth(width)
  try {
    localStorage.setItem(STORAGE_KEY, String(clamped))
  } catch {
    // Ignore storage quota or private-mode failures.
  }
  return clamped
}
