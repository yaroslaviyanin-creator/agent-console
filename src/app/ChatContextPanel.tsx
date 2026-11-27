import { useEffect, useState } from 'react'
import { Download, ExternalLink, FileArchive, FileCode, FileText, Image as ImageIcon, Link2, Paperclip, Plus, Sparkles, X } from 'lucide-react'
import type { MessageAttachment } from '../protocol/types'
import type { RemoteTransport } from '../transport/RemoteTransport'
import type { Turn } from './chatStore'
import { formatAttachmentSize, isRenderableImageAttachment } from './attachments'

const URL_RE = /https?:\/\/[^\s<>"'`]+/g
const TRAILING_PUNCT_RE = /[.,;:!?)\]>]+$/

export function collectChatAttachments(turns: Turn[]): MessageAttachment[] {
  const out: MessageAttachment[] = []
  const seen = new Set<string>()
  for (const turn of turns) {
    for (const att of turn.attachments ?? []) {
      const key = att.id ? `id:${att.id}` : `name:${att.name}:${att.sizeBytes ?? 0}`
      if (seen.has(key)) continue
      seen.add(key)
      out.push(att)
    }
  }
  return out
}

export function extractChatLinks(turns: Turn[]): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const scan = (text: string) => {
    if (!text) return
    for (const match of text.matchAll(URL_RE)) {
      const cleaned = match[0].replace(TRAILING_PUNCT_RE, '')
      if (!cleaned) continue
      try {
        const parsed = new URL(cleaned)
        if ((parsed.protocol === 'http:' || parsed.protocol === 'https:') && !seen.has(cleaned)) {
          seen.add(cleaned)
          out.push(cleaned)
        }
      } catch {
        // Ignore malformed URLs.
      }
    }
  }
  for (const turn of turns) {
    scan(turn.prompt)
    for (const part of turn.parts) {
      if (part.kind === 'text' || part.kind === 'thinking') scan(part.text)
    }
  }
  return out
}

export interface ChatContextSummary {
  providerLabel: string
  modelLabel: string
  turnsCount: number
  messagesCount: number
  totalTokens?: number
}

export function summarizeChatContext(
  turns: Turn[],
  providerLabel: string,
  modelLabel: string,
): ChatContextSummary {
  let messagesCount = 0
  let totalTokens = 0
  let hasUsage = false
  for (const turn of turns) {
    if (turn.prompt || (turn.attachments?.length ?? 0) > 0) messagesCount += 1
    if (turn.parts.length > 0 || turn.state === 'completed' || turn.state === 'stopped') messagesCount += 1
    const maybeUsage = (turn as Turn & { usage?: { inputTokens?: number; outputTokens?: number; totalTokens?: number } }).usage
    if (maybeUsage) {
      hasUsage = true
      totalTokens += maybeUsage.totalTokens ?? ((maybeUsage.inputTokens ?? 0) + (maybeUsage.outputTokens ?? 0))
    }
  }
  return {
    providerLabel,
    modelLabel,
    turnsCount: turns.length,
    messagesCount,
    ...(hasUsage ? { totalTokens } : {}),
  }
}

function PanelAttachmentThumb({
  att,
  remote,
  onOpen,
}: {
  att: MessageAttachment
  remote: RemoteTransport | null
  onOpen: () => void
}) {
  const [thumbUrl, setThumbUrl] = useState('')
  const isImage = isRenderableImageAttachment(att) && !!att.url && !!remote

  useEffect(() => {
    if (!isImage || !remote || !att.url) return
    const controller = new AbortController()
    let objectUrl = ''
    void remote.fetchAttachmentBlobUrl(att.url, controller.signal).then(url => {
      objectUrl = url
      setThumbUrl(url)
    }).catch(() => {})
    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [isImage, remote, att.url])

  const Icon = att.kind === 'archive' || att.name.toLowerCase().endsWith('.zip')
    ? FileArchive
    : att.kind === 'pdf' || att.kind === 'document'
      ? FileText
      : att.kind === 'text'
        ? FileCode
        : att.kind === 'image'
          ? ImageIcon
          : Paperclip

  return (
    <li className="context-file-item" title={att.warning ? `${att.name}: ${att.warning}` : att.name}>
      {isImage && thumbUrl ? (
        <img src={thumbUrl} alt={att.name} className="context-file-thumb" />
      ) : (
        <span className="context-file-icon" aria-hidden="true"><Icon size={14} /></span>
      )}
      <div className="context-file-meta">
        <strong>{att.name}</strong>
        {att.sizeBytes != null && <small>{formatAttachmentSize(att.sizeBytes)}</small>}
      </div>
      {att.url && remote && (
        <button
          type="button"
          className="icon-btn context-file-action"
          aria-label={`Открыть или скачать ${att.name}`}
          title={`Скачать ${att.name}`}
          onClick={onOpen}
        >
          <Download size={13} />
        </button>
      )}
    </li>
  )
}

export function ChatContextPanel({
  turns,
  providerLabel,
  modelLabel,
  remote,
  canAttach,
  onAttachClick,
  onClose,
}: {
  turns: Turn[]
  providerLabel: string
  modelLabel: string
  remote: RemoteTransport | null
  canAttach: boolean
  onAttachClick: () => void
  onClose: () => void
}) {
  const attachments = collectChatAttachments(turns)
  const links = extractChatLinks(turns)
  const summary = summarizeChatContext(turns, providerLabel, modelLabel)

  return (
    <div className="context-panel-inner">
      <div className="context-panel-head">
        <h2>Контекст чата</h2>
        <button type="button" className="icon-btn" aria-label="Закрыть контекстную панель" onClick={onClose}>
          <X size={16} />
        </button>
      </div>

      <section className="context-section">
        <div className="context-section-head">
          <h3>Источники и файлы</h3>
          {canAttach && (
            <button
              type="button"
              className="icon-btn context-add-btn"
              aria-label="Прикрепить файлы"
              title="Прикрепить файлы"
              onClick={onAttachClick}
            >
              <Plus size={14} />
            </button>
          )}
        </div>
        {attachments.length === 0 ? (
          <p className="context-empty">В этом чате пока нет файлов.</p>
        ) : (
          <ul className="context-file-list">
            {attachments.map((att, idx) => (
              <PanelAttachmentThumb
                key={att.id ?? `${att.name}-${idx}`}
                att={att}
                remote={remote}
                onOpen={() => { if (remote) void remote.downloadAttachment(att) }}
              />
            ))}
          </ul>
        )}
      </section>

      <section className="context-section">
        <div className="context-section-head">
          <h3>Ссылки</h3>
          {links.length > 0 && <span className="context-count">{links.length}</span>}
        </div>
        {links.length === 0 ? (
          <p className="context-empty">Ссылки из переписки не найдены.</p>
        ) : (
          <ul className="context-link-list">
            {links.map(url => (
              <li key={url}>
                <a href={url} target="_blank" rel="noopener noreferrer" title={url}>
                  <Link2 size={12} aria-hidden="true" />
                  <span>{url}</span>
                  <ExternalLink size={11} aria-hidden="true" />
                </a>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="context-section">
        <div className="context-section-head">
          <h3>Сводка по чату</h3>
          <Sparkles size={13} aria-hidden="true" />
        </div>
        <dl className="context-summary">
          <div><dt>Провайдер</dt><dd>{summary.providerLabel}</dd></div>
          <div><dt>Модель</dt><dd>{summary.modelLabel}</dd></div>
          <div><dt>Сообщений</dt><dd>{summary.messagesCount}</dd></div>
          {summary.totalTokens != null && (
            <div><dt>Токены</dt><dd>{summary.totalTokens.toLocaleString('ru-RU')}</dd></div>
          )}
        </dl>
      </section>
    </div>
  )
}
