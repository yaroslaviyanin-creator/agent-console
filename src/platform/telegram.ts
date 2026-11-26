import type WebApp from '@twa-dev/sdk'
import { useLayoutEffect, useRef } from 'react'

type TimerHandle = ReturnType<typeof globalThis.setTimeout> | number

export interface TelegramViewportBridge {
  viewportHeight?: number
  expand: () => void
  onEvent?: (eventType: string, eventHandler: () => void) => void
  offEvent?: (eventType: string, eventHandler: () => void) => void
}

export interface ViewportEnvironment {
  window: {
    innerHeight: number
    addEventListener: (type: string, listener: () => void) => void
    removeEventListener: (type: string, listener: () => void) => void
    visualViewport?: {
      addEventListener: (type: string, listener: () => void) => void
      removeEventListener: (type: string, listener: () => void) => void
    } | null
  }
  document: {
    visibilityState?: string
    documentElement: {
      style: {
        setProperty: (name: string, value: string) => void
      }
    }
    addEventListener: (type: string, listener: () => void) => void
    removeEventListener: (type: string, listener: () => void) => void
  }
  requestAnimationFrame: (cb: () => void) => number
  cancelAnimationFrame: (id: number) => void
  setTimeout: (cb: () => void, delayMs: number) => TimerHandle
  clearTimeout: (id: TimerHandle) => void
}

const SDK_SYNC_EVENTS = ['viewportChanged', 'fullscreenChanged', 'safeAreaChanged'] as const
const EXPAND_SYNC_DELAYS_MS = [150, 400] as const

let backButton: typeof WebApp.BackButton | undefined
let backAction: (() => void) | undefined
let telegramUserId: string | null = null
let cleanupViewport: (() => void) | undefined

function defaultViewportEnv(): ViewportEnvironment {
  return {
    window,
    document,
    requestAnimationFrame: globalThis.requestAnimationFrame?.bind(globalThis)
      ?? ((cb) => globalThis.setTimeout(cb, 0) as unknown as number),
    cancelAnimationFrame: globalThis.cancelAnimationFrame?.bind(globalThis)
      ?? ((id) => globalThis.clearTimeout(id)),
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  }
}

/**
 * Computes and tracks `--app-height` for Telegram Mini App viewports and cleans up all listeners on dispose.
 */
export function bindTelegramViewport(
  tg: TelegramViewportBridge,
  env: ViewportEnvironment = defaultViewportEnv(),
): () => void {
  let disposed = false
  let rafId: number | undefined
  const timerIds = new Set<TimerHandle>()

  const syncHeight = () => {
    if (disposed) return
    const height = Math.max(env.window.innerHeight || 0, tg.viewportHeight || 0)
    env.document.documentElement.style.setProperty(
      '--app-height', height > 0 && Number.isFinite(height) ? `${height}px` : '100dvh',
    )
  }

  const clearScheduledSync = () => {
    if (rafId !== undefined) {
      env.cancelAnimationFrame(rafId)
      rafId = undefined
    }
    for (const id of timerIds) {
      env.clearTimeout(id)
    }
    timerIds.clear()
  }

  const expandAndSync = () => {
    if (disposed) return
    try {
      tg.expand()
    } catch {
      // Ignore bridge errors if expand is unavailable in the current host state.
    }
    syncHeight()
    clearScheduledSync()
    rafId = env.requestAnimationFrame(() => {
      rafId = undefined
      syncHeight()
    })
    for (const delay of EXPAND_SYNC_DELAYS_MS) {
      const id = env.setTimeout(() => {
        timerIds.delete(id)
        syncHeight()
      }, delay)
      timerIds.add(id)
    }
  }

  const onActivated = () => {
    expandAndSync()
  }

  const onVisibilityChange = () => {
    if (env.document.visibilityState === 'visible') {
      expandAndSync()
    }
  }

  tg.onEvent?.('activated', onActivated)
  for (const eventName of SDK_SYNC_EVENTS) {
    tg.onEvent?.(eventName, syncHeight)
  }
  env.document.addEventListener('visibilitychange', onVisibilityChange)
  env.window.addEventListener('resize', syncHeight)
  env.window.visualViewport?.addEventListener('resize', syncHeight)

  syncHeight()

  return () => {
    if (disposed) return
    disposed = true
    clearScheduledSync()
    tg.offEvent?.('activated', onActivated)
    for (const eventName of SDK_SYNC_EVENTS) {
      tg.offEvent?.(eventName, syncHeight)
    }
    env.document.removeEventListener('visibilitychange', onVisibilityChange)
    env.window.removeEventListener('resize', syncHeight)
    env.window.visualViewport?.removeEventListener('resize', syncHeight)
  }
}

export function getTelegramUserId(): string | null {
  return telegramUserId
}

function syncBackButton() {
  if (backAction) backButton?.show()
  else backButton?.hide()
}

// Login and the connected workspace each own navigation while mounted.
export function useTelegramBack(visible: boolean, onBack: () => void) {
  const latest = useRef(onBack)
  useLayoutEffect(() => { latest.current = onBack })
  useLayoutEffect(() => {
    if (!visible) return
    backAction = () => latest.current()
    syncBackButton()
    return () => {
      backAction = undefined
      syncBackButton()
    }
  }, [visible])
}

export async function initializeTelegram(): Promise<void> {
  const launch = new URLSearchParams(location.hash.slice(1))
  if (!launch.has('tgWebAppPlatform') && !launch.has('tgWebAppData')) return
  try {
    const { default: webApp } = await import('@twa-dev/sdk')
    document.documentElement.dataset.telegram = 'true'
    const tgId = webApp.initDataUnsafe?.user?.id
    if (tgId) telegramUserId = String(tgId)
    if (webApp.isVersionAtLeast('6.1')) {
      backButton = webApp.BackButton
      backButton.onClick(() => backAction?.())
      syncBackButton()
    }
    webApp.ready()
    cleanupViewport?.()
    cleanupViewport = bindTelegramViewport({
      get viewportHeight() { return webApp.viewportHeight },
      expand: () => webApp.expand(),
      // The SDK's event typings lag the runtime, which handles visibility and safe area events.
      onEvent: (name, handler) => webApp.onEvent(name as Parameters<typeof webApp.onEvent>[0], handler),
      offEvent: (name, handler) => webApp.offEvent(name as Parameters<typeof webApp.offEvent>[0], handler),
    })
    webApp.expand()
  } catch {
    // An unavailable bridge must leave the ordinary web entry point usable.
  }
}
