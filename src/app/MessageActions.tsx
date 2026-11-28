import { useEffect, useRef, useState } from 'react'
import { Check, Copy, FileText, GitFork, Info, MoreHorizontal, Pencil, RefreshCw, Trash2 } from 'lucide-react'
import { deleteChatMessage, forkChatAt, providerLabels, regenerateTurn, type Turn } from './chatStore'

/** Strips common Markdown syntax to clean readable plain text. */
export function markdownToPlainText(md: string): string {
  return md
    .replace(/\r\n/g, '\n')
    .replace(/```[\w-]*\n([\s\S]*?)```/g, '$1')
    .replace(/~~~[\w-]*\n([\s\S]*?)~~~/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/^>\s?/gm, '')
    .replace(/^[-*_]{3,}\s*$/gm, '')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/__([^_]+)__/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1')
    .replace(/_([^_]+)_/g, '$1')
    .replace(/~~([^~]+)~~/g, '$1')
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}

export async function copyToClipboard(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fallback below when clipboard permission is unavailable.
  }
  if (typeof document === 'undefined') return false
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', 'true')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } finally {
    area.remove()
  }
  return ok
}

function providerLabel(id?: string): string {
  if (!id) return 'Неизвестно'
  if (id === 'stub') return 'StubProvider'
  return (providerLabels as Record<string, string>)[id] ?? id
}

const fullDateTime = new Intl.DateTimeFormat('ru-RU', {
  dateStyle: 'medium',
  timeStyle: 'medium',
})

export function AssistantMessageActions({
  chatId,
  turn,
  writable,
  canFork,
  busy,
  isLast = true,
}: {
  chatId: string
  turn: Turn
  writable: boolean
  canFork: boolean
  busy: boolean
  isLast?: boolean
}) {
  const [copiedMd, setCopiedMd] = useState(false)
  const [copiedPlain, setCopiedPlain] = useState(false)
  const [moreOpen, setMoreOpen] = useState(false)
  const [infoOpen, setInfoOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!moreOpen && !infoOpen) return
    const onPointerDown = (e: PointerEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setMoreOpen(false)
        setInfoOpen(false)
        setConfirmDelete(false)
      }
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setMoreOpen(false)
        setInfoOpen(false)
        setConfirmDelete(false)
      }
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [moreOpen, infoOpen])

  if (turn.state === 'running') return null

  const rawMarkdown = turn.parts
    .flatMap(p => (p.kind === 'text' ? [p.text] : []))
    .join('\n\n')
    .trim()

  const handleCopyMarkdown = async () => {
    if (!rawMarkdown) return
    if (await copyToClipboard(rawMarkdown)) {
      setCopiedMd(true)
      setTimeout(() => setCopiedMd(false), 1500)
    }
  }

  const handleCopyPlain = async () => {
    const plain = markdownToPlainText(rawMarkdown)
    if (!plain) return
    if (await copyToClipboard(plain)) {
      setCopiedPlain(true)
      setMoreOpen(false)
      setTimeout(() => setCopiedPlain(false), 1500)
    }
  }

  return (
    <div
      className={`msg-actions${isLast ? ' is-last' : ''}${moreOpen || infoOpen ? ' is-menu-open' : ''}`}
      ref={wrapRef}
      aria-label="Действия с ответом"
    >
      {rawMarkdown && (
        <button
          type="button"
          className={`msg-action-btn is-icon${copiedMd ? ' is-done' : ''}`}
          onClick={() => void handleCopyMarkdown()}
          title={copiedMd ? 'Скопировано' : 'Копировать'}
          aria-label={copiedMd ? 'Скопировано' : 'Копировать'}
        >
          {copiedMd ? <Check size={14} /> : <Copy size={14} />}
        </button>
      )}
      {writable && (
        <button
          type="button"
          className="msg-action-btn is-icon"
          disabled={busy}
          onClick={() => void regenerateTurn(chatId, turn.id)}
          title="Повторить ответ"
          aria-label="Повторить"
        >
          <RefreshCw size={14} />
        </button>
      )}
      {canFork && (
        <button
          type="button"
          className="msg-action-btn is-icon"
          onClick={() => void forkChatAt(chatId, turn.id)}
          title="Создать ветку отсюда"
          aria-label="Создать ветку отсюда"
        >
          <GitFork size={14} />
        </button>
      )}
      <div className="msg-action-anchor">
        <button
          type="button"
          className={`msg-action-btn is-icon${moreOpen ? ' is-open' : ''}`}
          aria-label="Ещё действия"
          title="Ещё действия"
          aria-expanded={moreOpen}
          aria-haspopup="menu"
          onClick={() => {
            setInfoOpen(false)
            setConfirmDelete(false)
            setMoreOpen(v => !v)
          }}
        >
          <MoreHorizontal size={14} />
        </button>
        {moreOpen && (
          <div className="msg-action-popover align-right" role="menu">
            {rawMarkdown && (
              <button type="button" role="menuitem" className="msg-popover-item" onClick={() => void handleCopyPlain()}>
                {copiedPlain ? <Check size={14} /> : <FileText size={14} />}
                <span>Скопировать как обычный текст</span>
              </button>
            )}
            <button
              type="button"
              role="menuitem"
              className="msg-popover-item"
              onClick={() => {
                setMoreOpen(false)
                setInfoOpen(v => !v)
              }}
            >
              <Info size={14} />
              <span>Инфо о сообщении</span>
            </button>
            {writable && !busy && (
              confirmDelete ? (
                <div className="msg-popover-confirm">
                  <span>Удалить сообщение?</span>
                  <div className="msg-popover-confirm-actions">
                    <button
                      type="button"
                      className="is-danger"
                      onClick={() => {
                        setMoreOpen(false)
                        setConfirmDelete(false)
                        void deleteChatMessage(chatId, turn.id)
                      }}
                    >
                      Удалить
                    </button>
                    <button type="button" onClick={() => setConfirmDelete(false)}>Отмена</button>
                  </div>
                </div>
              ) : (
                <button
                  type="button"
                  role="menuitem"
                  className="msg-popover-item is-danger"
                  onClick={() => setConfirmDelete(true)}
                >
                  <Trash2 size={14} />
                  <span>Удалить сообщение</span>
                </button>
              )
            )}
          </div>
        )}
        {infoOpen && (
          <div className="msg-info-popover align-right" role="dialog" aria-label="Информация о сообщении">
            <div className="msg-info-row"><span>Провайдер</span><strong>{providerLabel(turn.provider)}</strong></div>
            <div className="msg-info-row"><span>Модель</span><strong>{turn.model || (turn.modelId && turn.modelId !== 'default' ? turn.modelId : 'По умолчанию')}</strong></div>
            <div className="msg-info-row"><span>Время</span><strong>{fullDateTime.format(turn.time)}</strong></div>
            {turn.durationMs != null && (
              <div className="msg-info-row"><span>Длительность</span><strong>{(turn.durationMs / 1000).toFixed(1)} с</strong></div>
            )}
            {turn.usage && (
              <>
                <div className="msg-info-row">
                  <span>Вход</span>
                  <strong>
                    {turn.usage.inputTokens.toLocaleString('ru-RU')}
                    {turn.usage.cachedInputTokens > 0 ? ` (кэш: ${turn.usage.cachedInputTokens.toLocaleString('ru-RU')})` : ''}
                  </strong>
                </div>
                <div className="msg-info-row">
                  <span>Выход</span>
                  <strong>{turn.usage.outputTokens.toLocaleString('ru-RU')}</strong>
                </div>
                {(turn.usage.reasoningTokens ?? 0) > 0 && (
                  <div className="msg-info-row">
                    <span>Обдумывание</span>
                    <strong>{(turn.usage.reasoningTokens ?? 0).toLocaleString('ru-RU')}</strong>
                  </div>
                )}
                <div className="msg-info-row">
                  <span>Итого</span>
                  <strong>{(turn.usage.totalTokens ?? (turn.usage.inputTokens + turn.usage.outputTokens)).toLocaleString('ru-RU')}</strong>
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

export function UserMessageActions({
  prompt,
  writable,
  busy,
  onEdit,
}: {
  prompt: string
  writable: boolean
  busy: boolean
  onEdit: () => void
}) {
  const [copied, setCopied] = useState(false)
  if (!prompt.trim()) return null

  const handleCopy = async () => {
    if (await copyToClipboard(prompt)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }

  return (
    <div className="user-msg-actions" aria-label="Действия с запросом">
      <button
        type="button"
        className={`msg-action-btn${copied ? ' is-done' : ''}`}
        onClick={() => void handleCopy()}
        title="Скопировать запрос"
        aria-label="Скопировать запрос"
      >
        {copied ? <Check size={13} /> : <Copy size={13} />}
        <span>{copied ? 'Скопировано' : 'Копировать'}</span>
      </button>
      {writable && !busy && (
        <button
          type="button"
          className="msg-action-btn"
          onClick={onEdit}
          title="Редактировать и переотправить"
          aria-label="Редактировать"
        >
          <Pencil size={13} />
          <span>Редактировать</span>
        </button>
      )}
    </div>
  )
}
