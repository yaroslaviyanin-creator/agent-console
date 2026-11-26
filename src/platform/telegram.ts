import { useLayoutEffect, useRef } from 'react'
import type WebApp from '@twa-dev/sdk'

let backButton: typeof WebApp.BackButton | undefined
let backAction: (() => void) | undefined
let telegramUserId: string | null = null

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
    webApp.expand()
  } catch {
    // An unavailable bridge must leave the ordinary web entry point usable.
  }
}
