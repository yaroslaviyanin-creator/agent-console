import { useEffect, useState } from 'react'
import { Download, ExternalLink, FileArchive, FileCode, FileText, Image as ImageIcon, Link2, Paperclip, Plus, Sparkles, X } from 'lucide-react'
import type { MessageAttachment, MessageUsage, ServerModel, ServerProvider } from '../protocol/types'
import type { RemoteTransport } from '../transport/RemoteTransport'
import { resolveModelDisplay, type Turn } from './chatStore'
import { formatAttachmentSize, isRenderableImageAttachment } from './attachments'
import { useFileViewer } from './fileViewerStore'

const URL_RE = /https?:\/\/[^\s<>"'`]+/g
const TRAILING_PUNCT_RE = /[.,;:!?)\]>]+$/

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  antigravity: 'Antigravity',
  codex: 'Codex',
  claude: 'Claude',
  paid_api: 'Paid API',
  stub: 'StubProvider',
}

export function formatCompactTokens(value: number): string {
  const n = Math.max(0, Math.round(value))
  if (n < 1000) return String(n)
  if (n < 100_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}K`
  if (n < 1_000_000) return `${Math.round(n / 1000)}K`
  return `${(n / 1_000_000).toFixed(2).replace(/\.?0+$/, '')}M`
}

export function formatUsageTooltip(usage: MessageUsage): string {
  const lines: string[] = []
  const cachePart = usage.cachedInputTokens > 0
    ? ` (кэш: ${usage.cachedInputTokens.toLocaleString('ru-RU')})`
    : ''
  lines.push(`Вход: ${usage.inputTokens.toLocaleString('ru-RU')}${cachePart}`)
  lines.push(`Выход: ${usage.outputTokens.toLocaleString('ru-RU')}`)
  if ((usage.reasoningTokens ?? 0) > 0) {
    lines.push(`Обдумывание: ${(usage.reasoningTokens ?? 0).toLocaleString('ru-RU')}`)
  }
  const total = usage.totalTokens ?? (usage.inputTokens + usage.outputTokens)
  lines.push(`Итого: ${total.toLocaleString('ru-RU')}`)
  return lines.join(' · ')
}

export function formatUsageInline(usage: MessageUsage): string {
  const base = `↑ ${formatCompactTokens(usage.inputTokens)} · ↓ ${formatCompactTokens(usage.outputTokens)} ток.`
  if ((usage.reasoningTokens ?? 0) > 0) {
    return `${base} · мысли ${formatCompactTokens(usage.reasoningTokens ?? 0)}`
  }
  return base
}

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

export interface ModelUsageSummary {
  modelId: string
  modelLabel: string
  count: number
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningTokens: number
}

export interface ProviderUsageSummary {
  providerId: string
  providerLabel: string
  count: number
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningTokens: number
  models: ModelUsageSummary[]
}

export interface ChatContextSummary {
  providerLabel: string
  modelLabel: string
  turnsCount: number
  messagesCount: number
  hasDetailedUsage: boolean
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningTokens: number
  totalTokens?: number
  byProvider: ProviderUsageSummary[]
}

export function summarizeChatContext(
  turns: Turn[],
  providerLabel: string,
  modelLabel: string,
  serverModelsByProvider: Partial<Record<ServerProvider, ServerModel[]>> = {},
  defaultProviderId?: string,
): ChatContextSummary {
  let messagesCount = 0
  let inputTokens = 0
  let cachedInputTokens = 0
  let outputTokens = 0
  let reasoningTokens = 0
  let totalTokens = 0
  let hasUsage = false
  let hasDetailedUsage = false

  const providerMap = new Map<string, {
    providerId: string
    providerLabel: string
    count: number
    inputTokens: number
    cachedInputTokens: number
    outputTokens: number
    reasoningTokens: number
    models: Map<string, ModelUsageSummary>
  }>()

  for (const turn of turns) {
    if (turn.prompt || (turn.attachments?.length ?? 0) > 0) messagesCount += 1
    const hasAssistant = turn.parts.length > 0 || turn.state === 'completed' || turn.state === 'stopped'
    if (hasAssistant) messagesCount += 1

    const u = turn.usage
    if (u) {
      hasUsage = true
      const inTok = u.inputTokens ?? 0
      const cachedTok = u.cachedInputTokens ?? 0
      const outTok = u.outputTokens ?? 0
      const reasonTok = u.reasoningTokens ?? 0
      if (inTok > 0 || outTok > 0 || cachedTok > 0 || reasonTok > 0) {
        hasDetailedUsage = true
      }
      inputTokens += inTok
      cachedInputTokens += cachedTok
      outputTokens += outTok
      reasoningTokens += reasonTok
      totalTokens += u.totalTokens ?? (inTok + outTok)
    }

    if (hasAssistant) {
      const rawPid = (turn.provider ?? defaultProviderId ?? '').trim().toLowerCase()
      const pid = rawPid || 'unknown'
      const pLabel = pid !== 'unknown' ? (PROVIDER_DISPLAY_NAMES[pid] ?? pid) : providerLabel
      const resolved = resolveModelDisplay(pid !== 'unknown' ? pid : undefined, turn.modelId, serverModelsByProvider, turn.model)
      const rawMid = (resolved.modelId && resolved.modelId !== 'default' ? resolved.modelId : resolved.modelLabel) || modelLabel
      const mid = rawMid.trim().toLowerCase()
      const mLabel = resolved.modelLabel || modelLabel

      let pEntry = providerMap.get(pid)
      if (!pEntry) {
        pEntry = {
          providerId: pid,
          providerLabel: pLabel,
          count: 0,
          inputTokens: 0,
          cachedInputTokens: 0,
          outputTokens: 0,
          reasoningTokens: 0,
          models: new Map(),
        }
        providerMap.set(pid, pEntry)
      }
      pEntry.count += 1
      pEntry.inputTokens += u?.inputTokens ?? 0
      pEntry.cachedInputTokens += u?.cachedInputTokens ?? 0
      pEntry.outputTokens += u?.outputTokens ?? 0
      pEntry.reasoningTokens += u?.reasoningTokens ?? 0

      let mEntry = pEntry.models.get(mid)
      if (!mEntry) {
        mEntry = {
          modelId: mid,
          modelLabel: mLabel,
          count: 0,
          inputTokens: 0,
          cachedInputTokens: 0,
          outputTokens: 0,
          reasoningTokens: 0,
        }
        pEntry.models.set(mid, mEntry)
      }
      mEntry.count += 1
      mEntry.inputTokens += u?.inputTokens ?? 0
      mEntry.cachedInputTokens += u?.cachedInputTokens ?? 0
      mEntry.outputTokens += u?.outputTokens ?? 0
      mEntry.reasoningTokens += u?.reasoningTokens ?? 0
    }
  }

  const byProvider: ProviderUsageSummary[] = Array.from(providerMap.values()).map(p => ({
    providerId: p.providerId,
    providerLabel: p.providerLabel,
    count: p.count,
    inputTokens: p.inputTokens,
    cachedInputTokens: p.cachedInputTokens,
    outputTokens: p.outputTokens,
    reasoningTokens: p.reasoningTokens,
    models: Array.from(p.models.values()),
  }))

  return {
    providerLabel,
    modelLabel,
    turnsCount: turns.length,
    messagesCount,
    hasDetailedUsage,
    inputTokens,
    cachedInputTokens,
    outputTokens,
    reasoningTokens,
    ...(hasUsage ? { totalTokens } : {}),
    byProvider,
  }
}

function PanelAttachmentThumb({
  att,
  remote,
  onOpen,
  onDownload,
}: {
  att: MessageAttachment
  remote: RemoteTransport | null
  onOpen: () => void
  onDownload: () => void
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
    <li
      className="context-file-item is-clickable"
      role="button"
      tabIndex={0}
      title={att.warning ? `${att.name}: ${att.warning}` : `Открыть ${att.name}`}
      onClick={onOpen}
      onKeyDown={e => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
    >
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
          aria-label={`Скачать ${att.name}`}
          title={`Скачать ${att.name}`}
          onClick={e => {
            e.stopPropagation()
            onDownload()
          }}
        >
          <Download size={13} />
        </button>
      )}
    </li>
  )
}

function renderTokenPairs(inTok: number, outTok: number, reasonTok: number) {
  return (
    <>
      <span className="context-token-pair">↑ {formatCompactTokens(inTok)}</span>
      <span className="context-token-sep" aria-hidden="true">·</span>
      <span className="context-token-pair">↓ {formatCompactTokens(outTok)}</span>
      {reasonTok > 0 && (
        <>
          <span className="context-token-sep" aria-hidden="true">·</span>
          <span className="context-token-pair">мысли {formatCompactTokens(reasonTok)}</span>
        </>
      )}
    </>
  )
}

export function ChatContextPanel({
  turns,
  defaultProviderId,
  providerLabel,
  modelLabel,
  serverModelsByProvider = {},
  remote,
  canAttach,
  onAttachClick,
  onClose,
}: {
  turns: Turn[]
  defaultProviderId?: string
  providerLabel: string
  modelLabel: string
  serverModelsByProvider?: Partial<Record<ServerProvider, ServerModel[]>>
  remote: RemoteTransport | null
  canAttach: boolean
  onAttachClick: () => void
  onClose: () => void
}) {
  const attachments = collectChatAttachments(turns)
  const links = extractChatLinks(turns)
  const summary = summarizeChatContext(turns, providerLabel, modelLabel, serverModelsByProvider, defaultProviderId)
  const showBreakdown = summary.byProvider.length >= 2 || (summary.byProvider[0]?.models.length ?? 0) >= 2

  return (
    <div className="context-panel-inner">
      <div className="context-panel-head">
        <h2>Контекст чата</h2>
        <button type="button" className="icon-btn" aria-label="Закрыть контекстную панель" onClick={onClose}>
          <X size={16} />
        </button>
      </div>

      <section className={`context-section${attachments.length === 0 ? ' is-empty' : ''}`}>
        <div className="context-section-head">
          <h3>Источники и файлы</h3>
          <div className="context-section-meta">
            {attachments.length === 0 && <span className="context-empty-inline">Нет</span>}
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
        </div>
        {attachments.length > 0 && (
          <ul className="context-file-list">
            {attachments.map((att, idx) => (
              <PanelAttachmentThumb
                key={att.id ?? `${att.name}-${idx}`}
                att={att}
                remote={remote}
                onOpen={() => { useFileViewer.getState().openFile(att) }}
                onDownload={() => { if (remote) void remote.downloadAttachment(att) }}
              />
            ))}
          </ul>
        )}
      </section>

      <section className={`context-section${links.length === 0 ? ' is-empty' : ''}`}>
        <div className="context-section-head">
          <h3>Ссылки</h3>
          {links.length > 0 ? <span className="context-count">{links.length}</span> : <span className="context-empty-inline">Нет</span>}
        </div>
        {links.length > 0 && (
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
          {summary.hasDetailedUsage ? (
            <>
              <div>
                <dt>Вход</dt>
                <dd title={summary.cachedInputTokens > 0
                  ? `Вход: ${summary.inputTokens.toLocaleString('ru-RU')} (включая кэш: ${summary.cachedInputTokens.toLocaleString('ru-RU')})`
                  : `Вход: ${summary.inputTokens.toLocaleString('ru-RU')}`}
                >
                  ↑ {formatCompactTokens(summary.inputTokens)}
                  {summary.cachedInputTokens > 0 && (
                    <small className="context-token-sub"> кэш {formatCompactTokens(summary.cachedInputTokens)}</small>
                  )}
                </dd>
              </div>
              <div>
                <dt>Выход</dt>
                <dd title={`Выход: ${summary.outputTokens.toLocaleString('ru-RU')}`}>
                  ↓ {formatCompactTokens(summary.outputTokens)}
                </dd>
              </div>
              {summary.reasoningTokens > 0 && (
                <div>
                  <dt>Обдумывание</dt>
                  <dd title={`Обдумывание: ${summary.reasoningTokens.toLocaleString('ru-RU')}`}>
                    {formatCompactTokens(summary.reasoningTokens)}
                  </dd>
                </div>
              )}
            </>
          ) : summary.totalTokens != null ? (
            <div><dt>Итого</dt><dd>{summary.totalTokens.toLocaleString('ru-RU')}</dd></div>
          ) : null}
        </dl>

        {showBreakdown && (
          <div className="context-usage-breakdown">
            <h4>По провайдерам и моделям</h4>
            <ul className="context-provider-list">
              {summary.byProvider.map(p => (
                <li key={p.providerId} className="context-provider-item">
                  <div className="context-provider-head">
                    <strong>{p.providerLabel}</strong>
                    <span className="context-provider-count">{p.count} отв.</span>
                  </div>
                  <div className="context-provider-stats">
                    {renderTokenPairs(p.inputTokens, p.outputTokens, p.reasoningTokens)}
                  </div>
                  <ul className="context-model-list">
                    {p.models.map(m => (
                      <li key={m.modelId} className="context-model-item">
                        <div className="context-model-head">
                          <span className="context-model-name" title={m.modelLabel}>{m.modelLabel}</span>
                          <span className="context-model-count">{m.count}</span>
                        </div>
                        <div className="context-model-stats">
                          {renderTokenPairs(m.inputTokens, m.outputTokens, m.reasoningTokens)}
                        </div>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
    </div>
  )
}
