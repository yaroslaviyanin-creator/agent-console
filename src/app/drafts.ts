const DRAFT_PREFIX = 'ac-chat-draft.v1:'
export const NEW_CHAT_DRAFT_KEY = '__new__'

export function draftStorageKey(chatId: string | null | undefined): string {
  const id = (chatId ?? '').trim()
  return DRAFT_PREFIX + (id || NEW_CHAT_DRAFT_KEY)
}

export function readChatDraft(chatId: string | null | undefined): string {
  try {
    return localStorage.getItem(draftStorageKey(chatId)) ?? ''
  } catch {
    return ''
  }
}

export function writeChatDraft(chatId: string | null | undefined, text: string): void {
  try {
    const key = draftStorageKey(chatId)
    if (!text) {
      localStorage.removeItem(key)
    } else {
      localStorage.setItem(key, text)
    }
  } catch {
    // Ignore storage quota or SecurityError in restricted WebViews.
  }
}

export function clearChatDraft(chatId: string | null | undefined): void {
  try {
    localStorage.removeItem(draftStorageKey(chatId))
  } catch {
    // Ignore.
  }
}

export function hasNonEmptyChatDraft(chatId: string | null | undefined): boolean {
  return readChatDraft(chatId).trim().length > 0
}
