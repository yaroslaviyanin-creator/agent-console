/**
 * Detects Tauri desktop runtime and macOS host to enable native overlay titlebar offsets.
 */
export function initializePlatform(): void {
  if (typeof window === 'undefined' || typeof document === 'undefined') return
  const isTauri = '__TAURI_INTERNALS__' in window || '__TAURI__' in window
  if (!isTauri) return
  document.documentElement.dataset.tauri = 'true'
  const platform = `${navigator.platform ?? ''} ${navigator.userAgent ?? ''}`
  if (/mac/i.test(platform)) {
    document.documentElement.dataset.tauriMac = 'true'
  }
}
