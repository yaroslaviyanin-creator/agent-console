import { useTelegramBack } from '../platform/telegram'
import { RetryActions } from './RetryActions'
import { Billing } from './Billing'
import { Incoming, LimitActions, Quotas, ServerQuotas } from './Quotas'
import { AuthScene } from './AuthScene'
import { WorkspaceDialog } from './WorkspaceDialog'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUp, Brain, ChevronDown, ChevronRight, CodeXml, Cpu, GitFork, Globe, Hourglass, Laptop, Menu, Mic, MicOff, PanelLeft, PanelRight, Paperclip, Receipt, Square, Terminal } from 'lucide-react'
import { transport } from './runtime'
import { blockReason, useAccountStore } from './accountStore'
import { clearChatAttachmentError, clearSearchJump, demoPrompt, availableDevices, downloadChatMarkdown, editAndResendPrompt, loadFolderAttachments, models, providerLabels, checkAccountStatus, logout, newChat, reconnectServer, removeChatAttachment, resolveAvailableProviders, resolveModelDisplay, retryAnswer, retryHistory, selectChatModel, selectChatReasoning, sendPrompt, setChatDraftText, stopChat, switchChatProvider, toggleChatWebSearch, updateChat, uploadChatFiles, useChats } from './chatStore'
import { useAppearance } from './appearanceStore'
import { QuoteReplyButton } from './QuoteReplyButton'
import { SlashCommandMenu, applySlashTemplate, filterPromptTemplates, findSlashQuery } from './SlashCommandMenu'
import { useVoiceDictation } from './voiceInput'
import type { Chat, Turn } from './chatStore'
import type { Provider, ServerProvider } from '../protocol/types'
import { CHAT_ATTACHMENT_ACCEPT, MAX_CHAT_ATTACHMENTS } from './attachments'
import { AssistantArtifacts, DraftAttachmentsBar, TurnAttachments } from './AttachmentViews'
import { ChatContextPanel, collectChatAttachments, extractChatLinks, formatUsageInline, formatUsageTooltip } from './ChatContextPanel'
import { FileViewer } from './FileViewer'
import { ImageLightbox } from './ImageLightbox'
import { DEFAULT_VIEWER_WIDTH_PCT, useFileViewer } from './fileViewerStore'
import { isRenderableImageAttachment } from './attachments'
import { clampContextWidth, DEFAULT_CONTEXT_WIDTH, readContextWidth, saveContextWidth } from './contextWidth'
import { clampSidebarWidth, DEFAULT_SIDEBAR_WIDTH, readSidebarWidth, saveSidebarWidth } from './sidebarWidth'
import { AssistantMessageActions, UserMessageActions } from './MessageActions'
import { fallbackProviders } from '../transport/RemoteTransport'
import { EnvironmentPopover, ModelPopover } from './ComposerPopovers'
import type { ProviderOption } from './ProviderSelect'
import { defaultReasoningForProvider, groupServerModels } from './modelCatalog'
import { Sidebar } from './Sidebar'
import { SearchPalette } from './SearchPalette'
import { ToolCard } from './ToolCard'
import { RichText } from './Markdown'
import { AdminDialog } from './AdminDialog'
import { AmbientBackground } from './AmbientBackground'
import { UserAvatar } from './UserAvatar'

const mcpExamples = [
  ['Список файлов', 'Покажи файлы проекта'],
  ['Прочитать файл', 'Прочитай settings.ts'],
  ['Найти текст', 'Найди «timeout» в проекте'],
  ['Создать файл', 'Создай notes.md с текстом: Проверка тайм-аута'],
  ['Изменить тайм-аут', 'Установи тайм-аут в settings.ts на 3000 мс'],
  ['Запустить тесты', 'Запусти тесты'],
] as const

const clock = new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' })
const streamingConsoleFileDoneRe = /<console_file\s+name\s*=\s*(?:"([^"]*)"|'([^']*)')\s*>[\s\S]*?<\/console_file\s*>/gi
const streamingConsoleFileOpenRe = /<console_file\s+name\s*=\s*(?:"([^"]*)"|'([^']*)')\s*>[\s\S]*$/i

function formatStreamingDisplayText(text: string, running: boolean): string {
  if (!running || !text.toLowerCase().includes('<console_file')) return text
  const replaced = text.replace(streamingConsoleFileDoneRe, (_, d1, d2) => {
    const name = (d1 || d2 || 'файл').trim()
    return `\n\n> *Файл \`${name}\` подготовлен…*\n\n`
  })
  return replaced.replace(streamingConsoleFileOpenRe, (_, d1, d2) => {
    const name = (d1 || d2 || 'файл').trim()
    return `\n\n> *Генерация файла \`${name}\`…*`
  })
}

function webSourceLabel(url: string, title?: string): string {
  if (title && title.trim()) return title.trim()
  try {
    return new URL(url).hostname
  } catch {
    return url
  }
}

function webSourceStatusMeta(status: 'ok' | 'blocked' | 'js_required' | 'error'): { label: string; cls: string } {
  switch (status) {
    case 'ok': return { label: 'OK', cls: 'is-ok' }
    case 'blocked': return { label: 'Закрыто', cls: 'is-blocked' }
    case 'js_required': return { label: 'Нужен JS', cls: 'is-warn' }
    default: return { label: 'Ошибка', cls: 'is-err' }
  }
}

function progressLabel(durationMs?: number) {
  if (durationMs == null) return 'Ход работы'
  if (durationMs < 1000) return 'Выполнено меньше чем за секунду'
  const seconds = Math.floor(durationMs / 1000)
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor(seconds / 60) % 60
  return 'Выполнено за ' + [
    hours && `${hours} ч`, minutes && `${minutes} мин`, seconds % 60 && `${seconds % 60} с`,
  ].filter(Boolean).join(' ')
}

function Transcript({
  turn,
  chatId,
  writable,
  canFork,
  busy,
  retry,
  demo,
  isLast,
  highlighted,
}: {
  turn: Turn
  chatId: string
  writable: boolean
  canFork: boolean
  busy: boolean
  retry: (modelId: string) => void
  demo: boolean
  isLast: boolean
  highlighted?: boolean
}) {
  const w = useChats(s => s.workspace)!
  const remote = useChats(s => s.remote)
  const [editingPrompt, setEditingPrompt] = useState(false)
  const [editDraft, setEditDraft] = useState(turn.prompt)
  const authorId = turn.authorId ?? w.chats.find(c => c.id === chatId)?.ownerId
  const authorMember = authorId === w.user.id ? w.user : w.users.find(u => u.id === authorId)
  const author = authorId === w.user.id ? 'Вы' : authorMember?.displayName?.trim() || authorMember?.name || 'Участник'
  const avatarName = authorMember?.displayName?.trim() || authorMember?.name || author
  const isSelf = authorId === w.user.id
  const hasPrompt = Boolean(turn.prompt && turn.prompt.trim().length > 0)

  const submitEdit = () => {
    const trimmed = editDraft.trim()
    if (!trimmed) return
    setEditingPrompt(false)
    void editAndResendPrompt(chatId, turn.id, trimmed)
  }

  return <article className={'turn' + (highlighted ? ' is-search-highlight' : '')} data-turn-id={turn.id} data-user-msg-id={turn.userMessageId ?? ''}>
    {(hasPrompt || !!turn.attachments?.length) && (
      <div className={'user-turn-block' + (isSelf ? ' is-self' : '')}>
        <div className="person user-person">
          <UserAvatar name={avatarName} avatarData={authorMember?.avatarData} className="person-avatar" />
          <strong>{author}</strong>
          <time>{clock.format(turn.time)}</time>
        </div>
        {!!turn.attachments?.length && (
          <div className="user-turn-attachments">
            <TurnAttachments attachments={turn.attachments} remote={remote} />
          </div>
        )}
        {!!turn.webSources?.length && (
          <div className="web-source-chips" aria-label="Загруженные веб-страницы">
            {turn.webSources.map((ws, idx) => {
              const meta = webSourceStatusMeta(ws.status)
              return (
                <a
                  key={`${ws.url}-${idx}`}
                  href={ws.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className={`web-source-chip ${meta.cls}`}
                  title={ws.detail ? `${ws.url} (${ws.detail})` : ws.url}
                >
                  <Globe size={12} aria-hidden="true" />
                  <span className="web-source-title">{webSourceLabel(ws.url, ws.title)}</span>
                  <span className={`web-source-status ${meta.cls}`}>{meta.label}</span>
                </a>
              )
            })}
          </div>
        )}
        {editingPrompt ? (
          <div className="user-edit-box">
            <textarea
              rows={2}
              value={editDraft}
              aria-label="Редактировать запрос"
              onChange={e => setEditDraft(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Escape') {
                  e.preventDefault()
                  setEditingPrompt(false)
                  setEditDraft(turn.prompt)
                } else if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  submitEdit()
                }
              }}
            />
            <div className="user-edit-actions">
              <button type="button" className="alt-action" onClick={() => { setEditingPrompt(false); setEditDraft(turn.prompt) }}>Отмена</button>
              <button type="button" className="primary-action" disabled={!editDraft.trim()} onClick={submitEdit}>Отправить</button>
            </div>
          </div>
        ) : (
          <>
            {hasPrompt && (
              <div className="user-bubble">
                <div className="user-prompt">{turn.prompt}</div>
              </div>
            )}
            {hasPrompt && (
              <UserMessageActions
                prompt={turn.prompt}
                writable={writable}
                busy={busy}
                onEdit={() => {
                  setEditDraft(turn.prompt)
                  setEditingPrompt(true)
                }}
              />
            )}
          </>
        )}
      </div>
    )}
    <div className="person agent"><span className="agent-avatar"><CodeXml size={15}/></span><strong>{turn.model}</strong>{turn.retryOf && <span className="retry-label">Повтор ответа</span>}<time>{clock.format(turn.time)}</time></div>
    <div className="response">
      {turn.progress ? <details className="response-progress">
        <summary>{progressLabel(turn.durationMs)}<ChevronRight size={14}/></summary>
        <div className="response-progress-body"><RichText text={turn.progress}/></div>
      </details> : (turn.durationMs != null || turn.usage) && (
        <span className="response-duration" title={turn.usage ? formatUsageTooltip(turn.usage) : undefined}>
          {[turn.durationMs != null ? progressLabel(turn.durationMs) : '', turn.usage ? formatUsageInline(turn.usage) : ''].filter(Boolean).join(' · ')}
        </span>
      )}
      {turn.progress && turn.usage && (
        <span className="response-duration" title={formatUsageTooltip(turn.usage)}>{formatUsageInline(turn.usage)}</span>
      )}
      {turn.parts.map((p, i) => p.kind === 'tool' ? <ToolCard key={p.callId} chatId={chatId} turn={turn} entry={p}/> : p.kind === 'thinking'
      ? <details className="thinking" key={i}><summary><Brain size={14}/><span>Рассуждения</span><ChevronRight size={13}/></summary><p>{p.text}</p></details>
      : <RichText text={formatStreamingDisplayText(p.text, turn.state === 'running')} streaming={turn.state === 'running'} key={i}/>)}
      {!!turn.assistantAttachments?.length && (
        <AssistantArtifacts attachments={turn.assistantAttachments} remote={remote} />
      )}
      {turn.parts.length === 0 && turn.state === 'running' && <span role="status" className="waiting"><i aria-hidden="true"/>Агент подключается…</span>}
      {(turn.notice || turn.state === 'stopped') && <p role="status" className="notice">{turn.notice ?? 'Ответ остановлен.'}</p>}
      <AssistantMessageActions
        chatId={chatId}
        turn={turn}
        writable={writable}
        canFork={canFork}
        busy={busy}
        isLast={isLast}
      />
      {demo && turn.state === 'refused' && <RetryActions turn={turn} writable={writable} busy={busy} retry={retry}/>}
    </div>
  </article>
}

function Conversation({
  chat,
  billing,
}: {
  chat: Chat
  billing: () => void
}) {
  const {
    workspace: w,
    remote,
    connected,
    serverProviders,
    defaultServerProvider,
    providersLoaded,
    serverModelsByProvider,
    connectionError,
  } = useChats()
  const demoProvider = models.find(m => m.id === chat.model)?.provider ?? 'claude'
  const chatServerProvider: ServerProvider = chat.provider ?? defaultServerProvider ?? remote?.provider ?? 'stub'
  const provider: Provider = remote
    ? (chatServerProvider === 'stub' ? 'codex' : chatServerProvider)
    : demoProvider
  const serverModels = remote ? (serverModelsByProvider[chatServerProvider] ?? []) : []
  const providerDisabled = !!remote && !chat.indexedCodex && providersLoaded && !serverProviders.some(p => p.id === chatServerProvider && p.available)
  const sampleBlocked = useAccountStore(s => !!blockReason(s.quotas.claude))
  const reason = useAccountStore(s => remote ? undefined : blockReason(s.quotas[demoProvider]))
  const devices = availableDevices(w!, chat.id)
  const unavailableModel = !!remote && !chat.indexedCodex && !providerDisabled && chat.model !== 'default' && !serverModels.some(m => m.id === chat.model)
  const blocked = !!reason || !!remote && (!connected || !chat.historyLoaded || providerDisabled || unavailableModel)
  const availableProviders = resolveAvailableProviders(!!remote, serverProviders, defaultServerProvider)
  const canChangeProvider = (!remote || (chat.access === 'owner' && !chat.indexedCodex)) && availableProviders.length >= 2
  const canForkChat = !chat.indexedCodex && chat.access !== 'read'
  const scroll = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const input = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const composerRef = useRef<HTMLDivElement>(null)
  const dragDepthRef = useRef(0)
  const [draggingFiles, setDraggingFiles] = useState(false)
  const [openMenu, setOpenMenu] = useState<'model' | 'env' | null>(null)
  const [highlightedTurnId, setHighlightedTurnId] = useState<string | null>(null)
  const templates = useAppearance(s => s.templates)
  const [slashIndex, setSlashIndex] = useState(0)
  const [slashDismissed, setSlashDismissed] = useState(false)
  const [cursorPos, setCursorPos] = useState(0)
  const pendingSearchJump = useChats(s => s.pendingSearchJump)
  const webSearchEnabled = useChats(s => !!s.webSearchByChat[chat.id])
  const running = chat.turns.at(-1)?.state === 'running'
  const editable = chat.access === 'owner' || chat.access === 'write'
  const locked = running || !editable
  const slashMatch = !locked && !slashDismissed ? findSlashQuery(chat.draft, cursorPos) : null
  const slashResults = slashMatch ? filterPromptTemplates(templates, slashMatch.query) : []
  const clampedSlashIdx = slashResults.length > 0 ? Math.min(slashIndex, slashResults.length - 1) : 0

  const selectSlashTemplate = useCallback((tpl: (typeof templates)[number]) => {
    if (!slashMatch) return
    const { nextText, nextCursor } = applySlashTemplate(chat.draft, slashMatch, tpl)
    setChatDraftText(chat.id, nextText)
    setSlashDismissed(true)
    window.requestAnimationFrame(() => {
      const el = input.current
      if (el) {
        el.focus()
        el.setSelectionRange(nextCursor, nextCursor)
        setCursorPos(nextCursor)
      }
    })
  }, [slashMatch, chat.draft, chat.id])

  const voice = useVoiceDictation({
    getCurrentText: () => useChats.getState().chats.find(c => c.id === chat.id)?.draft ?? '',
    onUpdateText: text => setChatDraftText(chat.id, text),
  })
  const draftAttachments = chat.attachments ?? []
  const uploadingAttachments = draftAttachments.some(a => a.status === 'uploading')
  const hasReadyAttachments = draftAttachments.some(a => a.status === 'ready' && !!a.remote?.id)
  const canAttach = !!remote && editable && !locked && !chat.indexedCodex && draftAttachments.length < MAX_CHAT_ATTACHMENTS
  const last = chat.turns.at(-1)
  const pending = running && last?.parts.some(p => p.kind === 'tool' && p.permission && !p.result && !p.decision)
  useLayoutEffect(() => {
    const box = scroll.current
    if (box && follow.current && (!pendingSearchJump || pendingSearchJump.chatId !== chat.id)) {
      box.scrollTop = box.scrollHeight
    }
  }, [chat.turns, pendingSearchJump, chat.id])

  useEffect(() => {
    if (!pendingSearchJump || pendingSearchJump.chatId !== chat.id) return
    if (remote && !chat.historyLoaded) return
    const targetTurn = chat.turns.find(t =>
      t.id === pendingSearchJump.messageId || t.userMessageId === pendingSearchJump.messageId,
    )
    if (!targetTurn) {
      clearSearchJump()
      return
    }
    follow.current = false
    setHighlightedTurnId(targetTurn.id)
    const raf = window.requestAnimationFrame(() => {
      const container = scroll.current
      const el = container?.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(targetTurn.id)}"]`)
      el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    })
    clearSearchJump()
    const timer = window.setTimeout(() => setHighlightedTurnId(curr => (curr === targetTurn.id ? null : curr)), 2000)
    return () => {
      window.cancelAnimationFrame(raf)
      window.clearTimeout(timer)
    }
  }, [pendingSearchJump, chat.id, chat.historyLoaded, chat.turns, remote])
  useLayoutEffect(() => {
    const el = input.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`
  }, [chat.draft, editable])
  useEffect(() => {
    if (!openMenu) return
    const onPointerDown = (e: PointerEvent) => {
      if (!composerRef.current?.contains(e.target as Node)) setOpenMenu(null)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpenMenu(null)
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [openMenu])
  const submit = () => {
    if (!blocked && !running && editable && !uploadingAttachments && (chat.draft.trim() || hasReadyAttachments)) {
      follow.current = true
      setOpenMenu(null)
      void sendPrompt(chat.id, chat.draft)
      input.current?.focus()
    }
  }
  const demoProviderModels = models.filter(m => m.provider === provider)
  const demoModelGroups = Array.from(
    demoProviderModels.reduce((map, item) => {
      const key = item.shortLabel
      const list = map.get(key) ?? []
      list.push(item)
      map.set(key, list)
      return map
    }, new Map<string, (typeof demoProviderModels)[number][]>()),
    ([name, variants]) => ({ name, variants }),
  )
  const activeDemoGroup = demoModelGroups.find(g => g.variants.some(v => v.id === chat.model)) ?? demoModelGroups[0]
  const hasDemoVariantReasoning = Boolean(activeDemoGroup?.variants.some(v => 'reasoning' in v && v.reasoning))
  const demoReasoningOptions = hasDemoVariantReasoning ? [] : defaultReasoningForProvider(provider)
  const providerEntries = [
    ['antigravity', providerLabels.antigravity],
    ['codex', providerLabels.codex],
    ['claude', providerLabels.claude],
    ['paid_api', providerLabels.paid_api],
  ] as const
  const activeServerProviders = remote
    ? (serverProviders.length ? serverProviders : fallbackProviders(remote.provider).providers)
    : []
  const hasCurrentServerProvider = activeServerProviders.some(p => p.id === chatServerProvider)
  const providerOptions: ProviderOption[] = remote
    ? [
        ...activeServerProviders.map(p => ({
          id: p.id,
          label: p.id === 'stub' ? 'StubProvider' : (providerLabels[p.id] ?? p.id),
          available: p.available,
        })),
        ...(!hasCurrentServerProvider ? [{
          id: chatServerProvider,
          label: chatServerProvider === 'stub' ? 'StubProvider' : (providerLabels[chatServerProvider] ?? chatServerProvider),
          available: false,
        }] : []),
      ]
    : providerEntries.map(([id, label]) => ({ id, label, available: models.some(m => m.provider === id) }))

  const remoteGroups = remote ? groupServerModels(serverModels) : []
  const remoteActiveGroup = remote && chat.model !== 'default'
    ? remoteGroups.find(g => g.variants.some(v => v.id === chat.model))
    : undefined
  const remoteVariant = remoteActiveGroup?.variants.find(v => v.id === chat.model)
  const modelButtonName = remote
    ? chat.model === 'default'
      ? 'По умолчанию'
      : remoteActiveGroup?.name ?? chat.model
    : activeDemoGroup?.name ?? chat.model
  const activeDemoVariant = activeDemoGroup?.variants.find(v => v.id === chat.model)
  const rawReasoningLabel = remote
    ? remoteActiveGroup?.hasVariantReasoning
      ? remoteVariant?.reasoning
      : chat.reasoning || undefined
    : hasDemoVariantReasoning
      ? (activeDemoVariant && 'reasoning' in activeDemoVariant ? activeDemoVariant.reasoning : undefined)
      : chat.reasoning || undefined
  const reasoningPillLabel = rawReasoningLabel
    ? demoReasoningOptions.find(o => o.value === rawReasoningLabel)?.label ?? rawReasoningLabel
    : 'По умолчанию'
  const modelTriggerLabel = `${modelButtonName} · ${reasoningPillLabel}`

  const targetDeviceId = chat.target.mode === 'mcp' ? chat.target.deviceId : w!.deviceId
  const activeDevice = chat.target.mode === 'mcp'
    ? devices.find(d => d.id === targetDeviceId) ?? devices[0]
    : undefined
  const envTriggerLabel = chat.target.mode === 'mcp' ? 'Управление ПК' : 'Чат'

  const chatProviderLabel = chatServerProvider === 'stub' ? 'StubProvider' : (providerLabels[chatServerProvider] ?? chatServerProvider)
  const statusText = pending
    ? 'Ожидается разрешение'
    : running
      ? 'Агент отвечает…'
      : reason
        ? 'Ввод приостановлен: ' + reason
        : connectionError
          ? connectionError
          : remote && !connected
            ? 'Подключение к серверу…'
            : remote && !chat.historyLoaded
              ? 'Ожидание истории…'
              : providerDisabled
                ? `Провайдер ${chatProviderLabel} недоступен — отправка отключена`
                : unavailableModel
                  ? 'Выберите доступную модель'
                  : 'Готов к сообщению'
  const statusTone = (reason || connectionError || providerDisabled || unavailableModel)
    ? 'is-error'
    : (pending || running || (remote && (!connected || !chat.historyLoaded)))
      ? 'is-busy'
      : 'is-ready'

  return (
    <div
      className={`chat-drop-zone${draggingFiles ? ' is-dragging' : ''}`}
      onDragEnter={e => {
        if (!editable || !remote || chat.indexedCodex) return
        if (Array.from(e.dataTransfer.types).includes('Files')) {
          e.preventDefault()
          dragDepthRef.current++
          setDraggingFiles(true)
        }
      }}
      onDragOver={e => {
        if (!editable || !remote || chat.indexedCodex) return
        if (Array.from(e.dataTransfer.types).includes('Files')) {
          e.preventDefault()
        }
      }}
      onDragLeave={e => {
        if (!editable || !remote || chat.indexedCodex) return
        if (Array.from(e.dataTransfer.types).includes('Files')) {
          dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
          if (dragDepthRef.current === 0) setDraggingFiles(false)
        }
      }}
      onDrop={e => {
        dragDepthRef.current = 0
        setDraggingFiles(false)
        if (!editable || !remote || chat.indexedCodex) return
        if (e.dataTransfer.files?.length) {
          e.preventDefault()
          void uploadChatFiles(chat.id, e.dataTransfer.files)
        }
      }}
    >
    <div className="history" ref={scroll} onScroll={() => { const el = scroll.current; if (el) follow.current = el.scrollHeight - el.clientHeight - el.scrollTop < 48 }}>
      <QuoteReplyButton
        feedRef={scroll}
        enabled={editable && !chat.indexedCodex}
        onQuote={quoted => {
          const prev = chat.draft
          const next = prev ? `${prev.replace(/\s+$/, '')}\n\n${quoted}` : quoted
          setChatDraftText(chat.id, next)
          window.requestAnimationFrame(() => {
            const el = input.current
            if (el) {
              el.focus()
              el.setSelectionRange(next.length, next.length)
              setCursorPos(next.length)
            }
          })
        }}
      />
      <div className="thread">
        {chat.historyError && <div className="notice" role="alert">{chat.historyError} <button className="text-action" onClick={retryHistory}>Повторить загрузку</button></div>}
        {remote && !chat.historyLoaded ? !chat.historyError && (
          <div className="skeleton-stack" role="status" aria-label="Загружаю историю…">
            <span className="sr-only">Загружаю историю…</span>
            <div className="skeleton-line is-short" />
            <div className="skeleton-line is-medium" />
            <div className="skeleton-line is-long" />
          </div>
        ) : chat.turns.length ? chat.turns.map((turn, idx) => <Transcript
          key={turn.id}
          chatId={chat.id}
          turn={turn}
          writable={editable && !chat.indexedCodex}
          canFork={canForkChat}
          busy={running}
          demo={!remote}
          isLast={idx === chat.turns.length - 1}
          highlighted={highlightedTurnId === turn.id}
          retry={modelId => {
            follow.current = true
            void retryAnswer(chat.id, turn.id, modelId)
          }}
        />) :
        <div className="empty empty-hero">
          <div className="empty-squircle" aria-hidden="true"><CodeXml size={26} strokeWidth={2.1}/></div>
          <h2>{chat.indexedCodex ? 'В этом чате нет сообщений' : 'Начните разговор'}</h2>
          {remote ? editable && <p>Отправьте первое сообщение.</p> : editable && <>
            <p>Отправьте сообщение или попробуйте готовый демо-сценарий.</p>
            <div className="empty-suggestions">
              {['Предложи изменение settings.ts', 'Запусти тесты'].map(prompt => (
                <button key={prompt} disabled={!!reason} onClick={() => { updateChat(chat.id, c => ({ ...c, target: { mode: 'mcp', deviceId: w!.deviceId } })); void sendPrompt(chat.id, prompt) }}>
                  {prompt}<ChevronRight size={13}/>
                </button>
              ))}
              <button disabled={sampleBlocked} onClick={() => {
                updateChat(chat.id, c => ({ ...c, model: provider === 'claude' ? c.model : 'sonnet', target: { mode: 'chat' } }))
                void sendPrompt(chat.id, 'Объясни на примере, что такое тайм-аут подключения.')
              }}>Пример отказа модели<ChevronRight size={13}/></button>
            </div>
          </>}
        </div>}
      </div>
    </div>
    <footer className="composer-wrap"><div className="composer-inner">{editable ? <>
      {reason && !running && <LimitActions key={provider} w={w!} provider={provider} reason={reason} switchCodex={() => updateChat(chat.id,c => ({ ...c, model: 'codex' }))} billing={billing}/>}
      {providerDisabled && !running && <p className="notice" role="status">Провайдер {chatProviderLabel} недоступен на сервере. {canChangeProvider ? 'Выберите другой провайдер, чтобы продолжить чат.' : 'Отправка сообщений отключена.'}</p>}
      <div className="composer" ref={composerRef}>
        {slashResults.length > 0 && (
          <SlashCommandMenu
            templates={slashResults}
            activeIndex={clampedSlashIdx}
            onHover={setSlashIndex}
            onSelect={selectSlashTemplate}
          />
        )}
        {openMenu === 'model' && (
          <ModelPopover
            remoteMode={!!remote}
            provider={remote ? chatServerProvider : provider}
            providerOptions={providerOptions}
            model={chat.model}
            reasoning={chat.reasoning}
            unavailableModel={unavailableModel}
            disabled={locked}
            canChangeProvider={canChangeProvider}
            providerDisabled={providerDisabled}
            demoModelGroups={demoModelGroups}
            activeDemoGroup={activeDemoGroup}
            hasDemoVariantReasoning={hasDemoVariantReasoning}
            demoReasoningOptions={demoReasoningOptions}
            onSelectProvider={nextProvider => {
              if (remote) {
                void switchChatProvider(chat.id, nextProvider as ServerProvider)
                return
              }
              const first = models.find(m => m.provider === nextProvider)
              if (first) updateChat(chat.id, c => ({ ...c, model: first.id, reasoning: undefined }))
            }}
            onSelectModel={model => selectChatModel(chat.id, model)}
            onSelectReasoning={nextReasoning => selectChatReasoning(chat.id, nextReasoning)}
          />
        )}
        {openMenu === 'env' && (
          <EnvironmentPopover
            remoteMode={!!remote}
            mode={chat.target.mode}
            deviceId={chat.target.mode === 'mcp' ? chat.target.deviceId : w!.deviceId}
            devices={devices}
            ownerId={w!.user.id}
            disabled={locked}
            mcpExamples={mcpExamples}
            onSelectMode={nextMode => updateChat(chat.id, c => ({
              ...c,
              target: nextMode === 'chat' ? { mode: 'chat' } : { mode: 'mcp', deviceId: w!.deviceId },
            }))}
            onSelectDevice={deviceId => updateChat(chat.id, c => ({ ...c, target: { mode: 'mcp', deviceId } }))}
            onPickExample={draft => {
              updateChat(chat.id, c => ({ ...c, draft }))
              setOpenMenu(null)
              input.current?.focus()
            }}
          />
        )}
        <DraftAttachmentsBar
          attachments={draftAttachments}
          error={chat.attachmentError}
          disabled={running}
          onRemove={localId => removeChatAttachment(chat.id, localId)}
          onDismissError={() => clearChatAttachmentError(chat.id)}
        />
        <textarea disabled={!!reason} ref={input} rows={1} aria-label="Сообщение агенту" placeholder="Сообщение агенту…" value={chat.draft}
          onPointerDown={() => setOpenMenu(null)}
          onMouseDown={() => setOpenMenu(null)}
          onFocus={() => setOpenMenu(null)}
          onSelect={e => setCursorPos((e.target as HTMLTextAreaElement).selectionStart ?? 0)}
          onChange={e => {
            setSlashDismissed(false)
            setSlashIndex(0)
            setCursorPos(e.target.selectionStart ?? e.target.value.length)
            setChatDraftText(chat.id, e.target.value)
          }}
          onPaste={e => {
            if (!remote || locked || chat.indexedCodex) return
            const files: File[] = []
            for (const item of Array.from(e.clipboardData?.items ?? [])) {
              if (item.kind === 'file') {
                const f = item.getAsFile()
                if (f) files.push(f)
              }
            }
            if (files.length > 0) {
              e.preventDefault()
              void uploadChatFiles(chat.id, files)
            }
          }}
          onKeyDown={e => {
            if (slashResults.length > 0) {
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                setSlashIndex(i => (i + 1) % slashResults.length)
                return
              }
              if (e.key === 'ArrowUp') {
                e.preventDefault()
                setSlashIndex(i => (i - 1 + slashResults.length) % slashResults.length)
                return
              }
              if (e.key === 'Enter' || e.key === 'Tab') {
                e.preventDefault()
                const chosen = slashResults[clampedSlashIdx]
                if (chosen) selectSlashTemplate(chosen)
                return
              }
              if (e.key === 'Escape') {
                e.preventDefault()
                setSlashDismissed(true)
                return
              }
            }
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault()
              if (voice.listening) voice.stop()
              submit()
            }
          }}/>
        <div className="composer-controls">
          <div className="composer-triggers">
            {remote && !chat.indexedCodex && (
              <>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  accept={CHAT_ATTACHMENT_ACCEPT}
                  className="sr-only"
                  onChange={e => {
                    if (e.target.files?.length) void uploadChatFiles(chat.id, e.target.files)
                    e.target.value = ''
                  }}
                />
                <button
                  type="button"
                  className="composer-trigger composer-attach-btn"
                  disabled={!canAttach}
                  aria-label="Прикрепить файлы"
                  title="Прикрепить файлы (картинки, PDF, Word, Excel, PowerPoint, ODT, RTF, EPUB, код, ZIP)"
                  onClick={() => fileInputRef.current?.click()}
                >
                  <Paperclip size={13} aria-hidden="true" />
                </button>
              </>
            )}
            <button
              type="button"
              className={`composer-trigger${openMenu === 'model' ? ' is-open' : ''}`}
              aria-haspopup="dialog"
              aria-expanded={openMenu === 'model'}
              onClick={() => setOpenMenu(prev => prev === 'model' ? null : 'model')}
              title={modelTriggerLabel}
            >
              <Cpu size={13} aria-hidden="true" />
              <span>{modelTriggerLabel}</span>
              <ChevronDown size={12} aria-hidden="true" />
            </button>
            <button
              type="button"
              className={`composer-trigger${openMenu === 'env' ? ' is-open' : ''}`}
              aria-haspopup="dialog"
              aria-expanded={openMenu === 'env'}
              onClick={() => setOpenMenu(prev => prev === 'env' ? null : 'env')}
              title={activeDevice ? `${envTriggerLabel} · ${activeDevice.name}` : envTriggerLabel}
            >
              {chat.target.mode === 'mcp' ? <Laptop size={13} aria-hidden="true" /> : <Terminal size={13} aria-hidden="true" />}
              <span>{envTriggerLabel}</span>
              {chat.target.mode === 'mcp' && <i className={`env-dot ${activeDevice?.online ? 'is-online' : 'is-offline'}`} aria-hidden="true" />}
              <ChevronDown size={12} aria-hidden="true" />
            </button>
            {remote && !chat.indexedCodex && (
              <button
                type="button"
                className={`composer-trigger composer-search-btn${webSearchEnabled ? ' is-active' : ''}`}
                aria-pressed={webSearchEnabled}
                onClick={() => toggleChatWebSearch(chat.id)}
                title="Веб-поиск через SearXNG / внешний поиск"
              >
                <Globe size={13} aria-hidden="true" />
                <span>Поиск</span>
              </button>
            )}
          </div>
          <div className="composer-actions">
            {(statusTone === 'is-error' || (remote && (!connected || !chat.historyLoaded))) && (
              <span className={`status-lamp ${statusTone}`} role="status" title={statusText} aria-label={statusText} />
            )}
            {voice.supported && (
              <button
                type="button"
                className={`composer-mic-btn${voice.listening ? ' is-listening' : ''}`}
                disabled={!!reason || running}
                aria-label={voice.listening ? 'Остановить голосовой ввод' : 'Голосовой ввод'}
                title={voice.listening ? 'Остановить диктовку' : 'Голосовой ввод (ru-RU)'}
                onClick={voice.toggle}
              >
                {voice.listening ? <MicOff size={15} /> : <Mic size={15} />}
              </button>
            )}
            {running && !remote ? <button className="send stop" title="Остановить ответ" aria-label="Остановить ответ" onClick={() => stopChat(chat.id)}><Square size={13} fill="currentColor"/></button>
              : <button className="send" title="Отправить" aria-label="Отправить" disabled={blocked || running || uploadingAttachments || (!chat.draft.trim() && !hasReadyAttachments)} onClick={() => { if (voice.listening) voice.stop(); submit() }}><ArrowUp size={17}/></button>}
          </div>
        </div>
      </div>
    </> : <div className="access-note">{chat.access === 'fork' ? <button onClick={() => newChat(chat)}><GitFork size={15}/>Ответвить к себе</button> : 'Этот чат доступен для чтения'}</div>}</div></footer>
    </div>
  )
}
export function App() {
  const workspace = useChats(s => s.workspace)
  const restoring = useChats(s => s.restoring)
  const isBusy = useChats(s => {
    if (!s.workspace) return false
    const activeChat = s.chats.find(c => c.id === s.activeId)
    return activeChat?.turns.at(-1)?.state === 'running'
  })
  const [showAuthScene, setShowAuthScene] = useState(() => !workspace)
  const [skipSplash, setSkipSplash] = useState(false)
  const prevWorkspaceRef = useRef(workspace)

  useEffect(() => {
    if (prevWorkspaceRef.current && !workspace) {
      setSkipSplash(true)
      setShowAuthScene(true)
    }
    prevWorkspaceRef.current = workspace
  }, [workspace])

  const handleExited = useCallback(() => {
    setShowAuthScene(false)
  }, [])

  return <>
    <AmbientBackground dimmed={!!workspace && !showAuthScene} active={isBusy}/>
    {workspace && (workspace.user.role === 'pending' ? <PendingAccount/> : <Connected key={workspace.user.id}/>)}
    {(showAuthScene || !workspace) && (
      <AuthScene
        authenticated={!!workspace}
        restoring={restoring}
        skipSplash={skipSplash}
        onExited={handleExited}
      />
    )}
  </>
}
function PendingAccount() {
  const [busy, setBusy] = useState(false)
  const [feedback, setFeedback] = useState('')
  const [failed, setFailed] = useState(false)
  async function check() {
    setBusy(true)
    setFeedback('')
    setFailed(false)
    try {
      await checkAccountStatus()
      if (useChats.getState().workspace?.user.role === 'pending') setFeedback('Доступ ещё не выдан.')
    } catch (error) {
      setFailed(true)
      setFeedback(error instanceof Error ? error.message : 'Не удалось проверить статус.')
    } finally {
      setBusy(false)
    }
  }
  return <main className="login-screen">
    <div className="titlebar-drag-strip" data-tauri-drag-region aria-hidden="true" />
    <section className="login-card pending-account">
    <div className="auth-brand"><span className="brand-logo"><Hourglass size={20}/></span>Agent Console</div>
    <h1>Ожидание активации</h1>
    <p className="form-help">Аккаунт создан и ожидает выдачи доступа администратором.</p>
    {feedback && <p className={failed ? 'notice' : 'form-help'} role={failed ? 'alert' : 'status'}>{feedback}</p>}
    <div className="form-actions">
      <button type="button" className="primary-action" disabled={busy} onClick={() => void check()}>{busy ? 'Проверяю…' : 'Проверить статус'}</button>
      <button type="button" className="alt-action" onClick={logout}>Выйти</button>
    </div>
  </section></main>
}
function Connected() {
  const w = useChats(s => s.workspace)!
  const [view, setView] = useState<'chat' | 'billing'>('chat')
  const incoming = useAccountStore(s => s.requests.find(r => r.to.id === w.user.id && r.status === 'pending'))
  const [panel, setPanel] = useState<'share' | 'devices' | 'admin' | null>(null)
  const [shareChatId, setShareChatId] = useState<string | null>(null)
  const { chats, activeId, remote, connectionError, defaultServerProvider } = useChats()
  const isAdmin = remote != null && w.user.role === 'admin'
  const chat = chats.find(c => c.id === activeId)!
  const drawer = useRef<HTMLDialogElement>(null)
  const quotaMenu = useRef<HTMLDetailsElement>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [quotasOpen, setQuotasOpen] = useState(false)
  const [contextOpen, setContextOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const viewerFile = useFileViewer(s => s.file)
  const lightboxFile = useFileViewer(s => s.lightboxFile)
  const viewerWidthPct = useFileViewer(s => s.widthPct)
  const viewerExpanded = useFileViewer(s => s.expanded)
  const openFile = useFileViewer(s => s.openFile)
  const closeFile = useFileViewer(s => s.closeFile)
  const closeLightbox = useFileViewer(s => s.closeLightbox)
  const setViewerWidthPct = useFileViewer(s => s.setWidthPct)
  const toggleViewerExpanded = useFileViewer(s => s.toggleExpanded)
  useEffect(() => {
    closeFile()
    closeLightbox()
  }, [activeId, closeFile, closeLightbox])
  useEffect(() => {
    if (!contextOpen || !remote || !chat || chat.access !== 'owner' || !chat.folderId) return
    const state = useChats.getState()
    const folder = state.folders.find(f => f.id === chat.folderId)
    if (!folder || (folder.attachmentCount ?? 0) === 0) return
    if (state.folderAttachmentsById[folder.id] === undefined) {
      void loadFolderAttachments(folder.id).catch(() => {})
    }
  }, [contextOpen, remote, chat?.id, chat?.access, chat?.folderId])
  useTelegramBack(searchOpen || !!panel || drawerOpen || quotasOpen || !!lightboxFile || !!viewerFile || contextOpen || view === 'billing', () => {
    if (searchOpen) setSearchOpen(false)
    else if (panel) setPanel(null)
    else if (drawer.current?.open) drawer.current.close()
    else if (quotaMenu.current?.open) quotaMenu.current.open = false
    else if (lightboxFile) closeLightbox()
    else if (viewerFile) closeFile()
    else if (contextOpen) setContextOpen(false)
    else if (view === 'billing') setView('chat')
  })
  useEffect(() => {
    const onGlobalKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setSearchOpen(v => !v)
      }
    }
    window.addEventListener('keydown', onGlobalKeyDown)
    return () => window.removeEventListener('keydown', onGlobalKeyDown)
  }, [])
  useEffect(() => {
    if (!contextOpen || viewerFile || lightboxFile) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setContextOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [contextOpen, viewerFile, lightboxFile])
  const [collapsed, setCollapsed] = useState(false)
  const [sidebarWidth, setSidebarWidth] = useState(() => readSidebarWidth())
  const [resizingSidebar, setResizingSidebar] = useState(false)
  const [contextWidth, setContextWidth] = useState(() => readContextWidth())
  const [resizingContext, setResizingContext] = useState(false)
  const [resizingViewer, setResizingViewer] = useState(false)

  useEffect(() => {
    if (!resizingSidebar && !resizingContext && !resizingViewer) return
    const prevCursor = document.body.style.cursor
    const prevSelect = document.body.style.userSelect
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    return () => {
      document.body.style.cursor = prevCursor
      document.body.style.userSelect = prevSelect
    }
  }, [resizingSidebar, resizingContext, resizingViewer])

  const startSidebarResize = (e: React.PointerEvent<HTMLDivElement>) => {
    if (collapsed) return
    e.preventDefault()
    const startX = e.clientX
    const startW = sidebarWidth
    let latestW = startW
    setResizingSidebar(true)
    const onMove = (ev: PointerEvent) => {
      latestW = clampSidebarWidth(startW + (ev.clientX - startX))
      setSidebarWidth(latestW)
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      setResizingSidebar(false)
      saveSidebarWidth(latestW)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const startContextResize = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!contextOpen) return
    e.preventDefault()
    const startX = e.clientX
    const startW = contextWidth
    let latestW = startW
    setResizingContext(true)
    const onMove = (ev: PointerEvent) => {
      latestW = clampContextWidth(startW - (ev.clientX - startX))
      setContextWidth(latestW)
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      setResizingContext(false)
      saveContextWidth(latestW)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const startViewerResize = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!viewerFile) return
    e.preventDefault()
    setResizingViewer(true)
    const onMove = (ev: PointerEvent) => {
      const vw = Math.max(window.innerWidth, 1)
      setViewerWidthPct(((vw - ev.clientX) / vw) * 100)
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      setResizingViewer(false)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  useEffect(() => {
    if (remote) return
    const unsubscribe = transport.subscribeAccount(useAccountStore.getState().receive)
    const timer = setTimeout(() => {
      const state = useChats.getState()
      if (state.activeId === 'timeout' && !state.chats.find(c => c.id === 'timeout')?.turns.length) void sendPrompt('timeout', demoPrompt)
    }, 0)
    return () => { clearTimeout(timer); unsubscribe(); useAccountStore.setState({ quotas: {}, requests: [] }); stopChat(useChats.getState().activeId) }
  }, [remote])

  const isEmptyNewChat = !!chat && chat.turns.length === 0 && chat.title === 'Новый чат'
  const editableChat = !!chat && (chat.access === 'owner' || chat.access === 'write')
  const contextBadgeCount = chat
    ? collectChatAttachments(chat.turns).length + extractChatLinks(chat.turns).length
    : 0
  const openShareDialog = (chatId: string) => {
    setShareChatId(chatId)
    setPanel('share')
  }

  return <div
    className={'workspace ' + (collapsed ? 'collapsed' : '') + (resizingSidebar || resizingContext || resizingViewer ? ' is-resizing' : '')}
    style={{ '--sidebar-width': `${sidebarWidth}px` } as React.CSSProperties}
  >
    <aside className="desktop-side"><Sidebar openBilling={() => setView('billing')} openSettings={() => setPanel('devices')} openShare={openShareDialog} openSearch={() => setSearchOpen(true)} close={() => setView('chat')}/></aside>
    {!collapsed && (
      <div
        className="sidebar-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Изменить ширину боковой панели"
        aria-valuemin={200}
        aria-valuemax={420}
        aria-valuenow={sidebarWidth}
        onPointerDown={startSidebarResize}
        onDoubleClick={() => { setSidebarWidth(DEFAULT_SIDEBAR_WIDTH); saveSidebarWidth(DEFAULT_SIDEBAR_WIDTH) }}
      />
    )}
    <dialog ref={drawer} onClose={() => setDrawerOpen(false)} className="drawer" aria-label="Список чатов" onClick={e => { if (e.target === drawer.current) drawer.current.close() }}><Sidebar openBilling={() => { drawer.current?.close(); setView('billing') }} openSettings={() => { drawer.current?.close(); setPanel('devices') }} openAdmin={isAdmin ? () => { drawer.current?.close(); setPanel('admin') } : undefined} openShare={id => { drawer.current?.close(); openShareDialog(id) }} openSearch={() => { drawer.current?.close(); setSearchOpen(true) }} close={() => { drawer.current?.close(); setView('chat') }}/></dialog>
    <main className="main">
      <header className="chat-heading unified-header" data-tauri-drag-region>
        <div className="heading-inner">
          <div className="heading-left">
            <button className="icon-btn desk-toggle" aria-label="Переключить боковую панель" onClick={() => setCollapsed(!collapsed)}><PanelLeft size={17}/></button>
            <button className="icon-btn mobile" aria-label="Открыть список чатов" onClick={() => { drawer.current?.showModal(); setDrawerOpen(true) }}><Menu size={18}/></button>
            {view === 'billing' ? (
              <>
                <div className="heading-icon"><Receipt size={18} /></div>
                <div className="heading-text">
                  <h1>Биллинг API</h1>
                  <div className="heading-meta"><i />Демо-сессия<span>/</span><span>Учёт и лимиты</span></div>
                </div>
              </>
            ) : chat && !isEmptyNewChat ? (
              <>
                <div className="heading-icon"><Terminal size={18} /></div>
                <div className="heading-text">
                  <h1 title={chat.title}>{chat.title}</h1>
                  <div className="heading-meta">
                    <span>{chat.indexedCodex ? 'История Codex' : chat.access === 'owner' ? 'Личный чат' : 'Общий чат'}</span>
                    {!editableChat && chat.model && <><span>/</span><span className="heading-model-tag">{chat.model}</span></>}
                  </div>
                </div>
              </>
            ) : null}
          </div>
          <div className="topbar-actions">
            <div className="topbar-desktop-only">
              {remote ? <ServerQuotas menuRef={quotaMenu} onOpenChange={setQuotasOpen}/> : <Quotas userId={w.user.id} menuRef={quotaMenu} onOpenChange={setQuotasOpen}/> }
              {isAdmin && <button className="alt-action admin-entry" onClick={() => setPanel('admin')}>Админка</button>}
            </div>
            {view === 'chat' && chat && (
              <button
                type="button"
                className={`icon-btn context-toggle-btn${contextOpen ? ' is-open' : ''}`}
                aria-label="Переключить контекстную панель"
                aria-expanded={contextOpen}
                title="Источники, ссылки и сводка по чату"
                onClick={() => setContextOpen(v => !v)}
              >
                <PanelRight size={17} />
                {contextBadgeCount > 0 && <span className="topbar-panel-badge">{contextBadgeCount}</span>}
              </button>
            )}
          </div>
        </div>
      </header>
      {connectionError && (
        <p className="notice connection-error" role="alert">
          <span>{connectionError}</span>
          <button type="button" className="text-action" onClick={reconnectServer}>Повторить</button>
        </p>
      )}
      {!remote && incoming && <Incoming key={incoming.requestId} request={incoming}/>}
      {view === 'billing' ? <Billing back={() => setView('chat')}/> : chat ? <Conversation
        key={chat.id}
        chat={chat}
        billing={() => setView('billing')}
      /> : <div className="empty empty-hero thread"><div className="empty-squircle" aria-hidden="true"><CodeXml size={26} strokeWidth={2.1}/></div><h2>Пока нет чатов</h2><p>Создайте первый диалог, чтобы начать работу с агентом.</p><button onClick={() => void newChat()}>Создать чат</button></div>}
      {panel === 'admin' && isAdmin && <AdminDialog admin={remote.admin} userId={w.user.id} close={() => setPanel(null)}/>}
      {panel && panel !== 'admin' && <WorkspaceDialog kind={panel} w={w} chatId={panel === 'share' ? (shareChatId ?? activeId) : activeId} close={() => { setPanel(null); setShareChatId(null) }}/>}
    </main>
    {view === 'chat' && chat && (
      viewerFile ? (
        <>
          <div
            className="context-resizer fv-resizer"
            role="separator"
            aria-orientation="vertical"
            aria-label="Изменить ширину просмотрщика файла"
            aria-valuemin={25}
            aria-valuemax={65}
            aria-valuenow={Math.round(viewerExpanded ? 65 : viewerWidthPct)}
            onPointerDown={startViewerResize}
            onDoubleClick={() => setViewerWidthPct(DEFAULT_VIEWER_WIDTH_PCT)}
          />
          <aside
            className={`file-viewer-side is-open${resizingViewer ? ' is-resizing' : ''}`}
            aria-label={`Просмотр файла ${viewerFile.name}`}
            style={{ '--viewer-width': `${viewerExpanded ? 65 : viewerWidthPct}vw` } as React.CSSProperties}
          >
            <FileViewer
              file={viewerFile}
              remote={remote}
              expanded={viewerExpanded}
              onToggleExpand={toggleViewerExpanded}
              onClose={closeFile}
            />
          </aside>
        </>
      ) : (
        <>
          <div
            className={`context-backdrop${contextOpen ? ' is-open' : ''}`}
            onClick={() => setContextOpen(false)}
            aria-hidden="true"
          />
          {contextOpen && (
            <div
              className="context-resizer"
              role="separator"
              aria-orientation="vertical"
              aria-label="Изменить ширину контекстной панели"
              aria-valuemin={240}
              aria-valuemax={460}
              aria-valuenow={contextWidth}
              onPointerDown={startContextResize}
              onDoubleClick={() => { setContextWidth(DEFAULT_CONTEXT_WIDTH); saveContextWidth(DEFAULT_CONTEXT_WIDTH) }}
            />
          )}
          <aside
            className={`chat-context-side${contextOpen ? ' is-open' : ''}${resizingContext ? ' is-resizing' : ''}`}
            aria-label="Контекстная панель чата"
            aria-hidden={!contextOpen}
            style={{ '--context-width': `${contextWidth}px` } as React.CSSProperties}
          >
            <ChatContextPanel
              turns={chat.turns}
              defaultProviderId={chat.provider ?? defaultServerProvider ?? remote?.provider ?? 'stub'}
              providerLabel={(() => {
                const p = chat.provider ?? defaultServerProvider ?? remote?.provider ?? 'stub'
                return p === 'stub' ? 'StubProvider' : (providerLabels[p] ?? p)
              })()}
              modelLabel={(() => {
                const p = chat.provider ?? defaultServerProvider ?? remote?.provider ?? 'stub'
                return resolveModelDisplay(p, chat.model, useChats.getState().serverModelsByProvider).modelLabel
              })()}
              serverModelsByProvider={useChats.getState().serverModelsByProvider}
              remote={remote}
              canAttach={!!remote && editableChat && !chat.indexedCodex}
              onAttachClick={() => document.querySelector<HTMLInputElement>('.composer input[type="file"]')?.click()}
              onExportMarkdown={() => downloadChatMarkdown(chat)}
              projectFolder={(() => {
                if (chat.access !== 'owner' || !chat.folderId) return null
                return useChats.getState().folders.find(f => f.id === chat.folderId) ?? null
              })()}
              projectFiles={(() => {
                if (chat.access !== 'owner' || !chat.folderId) return undefined
                return useChats.getState().folderAttachmentsById[chat.folderId]
              })()}
              onClose={() => setContextOpen(false)}
            />
          </aside>
        </>
      )
    )}
    {view === 'chat' && chat && lightboxFile && (
      <ImageLightbox
        file={lightboxFile}
        images={collectChatAttachments(chat.turns).filter(att => isRenderableImageAttachment(att) && !!att.url)}
        remote={remote}
        onSelect={openFile}
        onClose={closeLightbox}
      />
    )}
    <SearchPalette
      open={searchOpen}
      onClose={() => setSearchOpen(false)}
      onAfterSelect={() => {
        setView('chat')
        drawer.current?.close()
      }}
    />
  </div>
}
