import { useTelegramBack } from '../platform/telegram'
import { RetryActions } from './RetryActions'
import { Billing } from './Billing'
import { Incoming, LimitActions, Quotas, ServerQuotas } from './Quotas'
import { AuthScene } from './AuthScene'
import { WorkspaceDialog } from './WorkspaceDialog'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUp, Brain, ChevronDown, ChevronRight, CodeXml, Cpu, GitFork, Hourglass, Image as ImageIcon, Laptop, Menu, PanelLeft, Paperclip, Share2, Square, Terminal } from 'lucide-react'
import { transport } from './runtime'
import { blockReason, useAccountStore } from './accountStore'
import { demoPrompt, availableDevices, models, providerLabels, checkAccountStatus, logout, newChat, retryAnswer, retryHistory, selectChatModel, selectChatReasoning, sendPrompt, stopChat, switchChatProvider, updateChat, useChats } from './chatStore'
import type { Chat, Turn } from './chatStore'
import type { Provider, ServerProvider } from '../protocol/types'
import { fallbackProviders } from '../transport/RemoteTransport'
import { EnvironmentPopover, ModelPopover } from './ComposerPopovers'
import type { ProviderOption } from './ProviderSelect'
import { defaultReasoningForProvider, groupServerModels } from './modelCatalog'
import { Sidebar } from './Sidebar'
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

function Transcript({ turn, chatId, writable, busy, retry, demo }: { turn: Turn; chatId: string; writable: boolean; busy: boolean; retry: (modelId: string) => void; demo: boolean }) {
  const w = useChats(s => s.workspace)!
  const authorId = turn.authorId ?? w.chats.find(c => c.id === chatId)?.ownerId
  const authorMember = authorId === w.user.id ? w.user : w.users.find(u => u.id === authorId)
  const author = authorId === w.user.id ? 'Вы' : authorMember?.displayName?.trim() || authorMember?.name || 'Участник'
  const avatarName = authorMember?.displayName?.trim() || authorMember?.name || author
  const isSelf = authorId === w.user.id
  return <article className="turn">
    {(turn.prompt || !!turn.attachments?.length) && (
      <div className={'user-turn-block' + (isSelf ? ' is-self' : '')}>
        <div className="person user-person">
          <UserAvatar name={avatarName} avatarData={authorMember?.avatarData} className="person-avatar" />
          <strong>{author}</strong>
          <time>{clock.format(turn.time)}</time>
        </div>
        <div className="user-bubble">
          {!!turn.attachments?.length && <ul className="message-attachments" aria-label="Вложения">
            {turn.attachments.map((attachment, index) => {
              const Icon = attachment.kind === 'image' ? ImageIcon : Paperclip
              return <li key={index} title={attachment.name}><Icon size={14} aria-hidden="true"/><span>{attachment.name}</span></li>
            })}
          </ul>}
          <div className="user-prompt">{turn.prompt}</div>
        </div>
      </div>
    )}
    <div className="person agent"><span className="agent-avatar"><CodeXml size={15}/></span><strong>{turn.model}</strong>{turn.retryOf && <span className="retry-label">Повтор ответа</span>}<time>{clock.format(turn.time)}</time></div>
    <div className="response">
      {turn.progress ? <details className="response-progress">
        <summary>{progressLabel(turn.durationMs)}<ChevronRight size={14}/></summary>
        <div className="response-progress-body"><RichText text={turn.progress}/></div>
      </details> : turn.durationMs != null && <span className="response-duration">{progressLabel(turn.durationMs)}</span>}
      {turn.parts.map((p, i) => p.kind === 'tool' ? <ToolCard key={p.callId} chatId={chatId} turn={turn} entry={p}/> : p.kind === 'thinking'
      ? <details className="thinking" key={i}><summary><Brain size={14}/><span>Рассуждения</span><ChevronRight size={13}/></summary><p>{p.text}</p></details>
      : <RichText text={p.text} key={i}/>)}
      {turn.parts.length === 0 && turn.state === 'running' && <span role="status" className="waiting"><i aria-hidden="true"/>Агент подключается…</span>}
      {(turn.notice || turn.state === 'stopped') && <p role="status" className="notice">{turn.notice ?? 'Ответ остановлен.'}</p>}
      {demo && <RetryActions turn={turn} writable={writable} busy={busy} retry={retry}/>}
    </div>
  </article>
}

function Conversation({ chat, share, billing }: { chat: Chat; share: () => void; billing: () => void }) {
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
  const canChangeProvider = !remote || (chat.access === 'owner' && !chat.indexedCodex)
  const scroll = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const input = useRef<HTMLTextAreaElement>(null)
  const composerRef = useRef<HTMLDivElement>(null)
  const [openMenu, setOpenMenu] = useState<'model' | 'env' | null>(null)
  const running = chat.turns.at(-1)?.state === 'running'
  const editable = chat.access === 'owner' || chat.access === 'write'
  const locked = running || !editable
  const last = chat.turns.at(-1)
  const pending = running && last?.parts.some(p => p.kind === 'tool' && p.permission && !p.result && !p.decision)
  useLayoutEffect(() => {
    const box = scroll.current
    if (box && follow.current) box.scrollTop = box.scrollHeight
  }, [chat.turns])
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
    if (!blocked && !running && editable && chat.draft.trim()) {
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

  return <>
    <header className="chat-heading"><div className="heading-inner"><div className="heading-icon"><Terminal size={19}/></div><div className="heading-text"><h1 title={chat.title}>{chat.title}</h1><div className="heading-meta"><i/>{remote ? 'Сервер' : 'Демо-сессия'}<span>/</span><span>{chat.indexedCodex ? 'История Codex' : chat.access === 'owner' ? 'Личный чат' : 'Общий чат'}</span>{!editable && chat.model && <><span>/</span><span className="heading-model-tag">{chat.model}</span></>}</div></div>{!chat.indexedCodex && chat.access === 'owner' && <button className="share-action" onClick={share} aria-label="Поделиться"><Share2 size={15}/><span>Поделиться</span></button>}</div></header>
    <div className="history" ref={scroll} onScroll={() => { const el = scroll.current; if (el) follow.current = el.scrollHeight - el.clientHeight - el.scrollTop < 48 }}>
      <div className="thread">
        {chat.historyError && <div className="notice" role="alert">{chat.historyError} <button className="text-action" onClick={retryHistory}>Повторить загрузку</button></div>}
        {remote && !chat.historyLoaded ? !chat.historyError && (
          <div className="skeleton-stack" role="status" aria-label="Загружаю историю…">
            <span className="sr-only">Загружаю историю…</span>
            <div className="skeleton-line is-short" />
            <div className="skeleton-line is-medium" />
            <div className="skeleton-line is-long" />
          </div>
        ) : chat.turns.length ? chat.turns.map(turn => <Transcript key={turn.id} chatId={chat.id} turn={turn} writable={editable} busy={running} demo={!remote} retry={modelId => {
        follow.current = true
        void retryAnswer(chat.id, turn.id, modelId)
      }}/>) :
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
        <textarea disabled={!!reason} ref={input} rows={1} aria-label="Сообщение агенту" placeholder="Сообщение агенту…" value={chat.draft}
          onChange={e => updateChat(chat.id, c => ({ ...c, draft: e.target.value }))}
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); submit() } }}/>
        <div className="composer-controls">
          <div className="composer-triggers">
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
          </div>
          <div className="composer-actions">
            <span className={`status-lamp ${statusTone}`} role="status" title={statusText} aria-label={statusText} />
            {running && !remote ? <button className="send stop" title="Остановить ответ" aria-label="Остановить ответ" onClick={() => stopChat(chat.id)}><Square size={13} fill="currentColor"/></button>
              : <button className="send" title="Отправить" aria-label="Отправить" disabled={blocked || running || !chat.draft.trim()} onClick={submit}><ArrowUp size={17}/></button>}
          </div>
        </div>
      </div>
    </> : <div className="access-note">{chat.access === 'fork' ? <button onClick={() => newChat(chat)}><GitFork size={15}/>Ответвить к себе</button> : 'Этот чат доступен для чтения'}</div>}</div></footer>
  </>
}
export function App() {
  const workspace = useChats(s => s.workspace)
  const restoring = useChats(s => s.restoring)
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
    <AmbientBackground dimmed={!!workspace && !showAuthScene}/>
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
  const { chats, activeId, remote, connectionError } = useChats()
  const isAdmin = remote != null && w.user.role === 'admin'
  const chat = chats.find(c => c.id === activeId)!
  const drawer = useRef<HTMLDialogElement>(null)
  const quotaMenu = useRef<HTMLDetailsElement>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [quotasOpen, setQuotasOpen] = useState(false)
  useTelegramBack(!!panel || drawerOpen || quotasOpen || view === 'billing', () => {
    if (panel) setPanel(null)
    else if (drawer.current?.open) drawer.current.close()
    else if (quotaMenu.current?.open) quotaMenu.current.open = false
    else if (view === 'billing') setView('chat')
  })
  const [collapsed, setCollapsed] = useState(false)
  useEffect(() => {
    if (remote) return
    const unsubscribe = transport.subscribeAccount(useAccountStore.getState().receive)
    const timer = setTimeout(() => {
      const state = useChats.getState()
      if (state.activeId === 'timeout' && !state.chats.find(c => c.id === 'timeout')?.turns.length) void sendPrompt('timeout', demoPrompt)
    }, 0)
    return () => { clearTimeout(timer); unsubscribe(); useAccountStore.setState({ quotas: {}, requests: [] }); stopChat(useChats.getState().activeId) }
  }, [remote])
  return <div className={'workspace ' + (collapsed ? 'collapsed' : '')}>
    <aside className="desktop-side"><Sidebar openBilling={() => setView('billing')} openSettings={() => setPanel('devices')} close={() => setView('chat')}/></aside>
    <dialog ref={drawer} onClose={() => setDrawerOpen(false)} className="drawer" aria-label="Список чатов" onClick={e => { if (e.target === drawer.current) drawer.current.close() }}><Sidebar openBilling={() => { drawer.current?.close(); setView('billing') }} openSettings={() => { drawer.current?.close(); setPanel('devices') }} close={() => { drawer.current?.close(); setView('chat') }}/></dialog>
    <main className="main"><div className="topbar" data-tauri-drag-region><button className="icon-btn desk-toggle" aria-label="Переключить боковую панель" onClick={() => setCollapsed(!collapsed)}><PanelLeft size={17}/></button><button className="icon-btn mobile" aria-label="Открыть список чатов" onClick={() => { drawer.current?.showModal(); setDrawerOpen(true) }}><Menu size={18}/></button>
      <span className="crumb" data-tauri-drag-region>Рабочее пространство</span><ChevronRight className="crumb-arrow" size={13} aria-hidden="true"/><span className="topbar-section" data-tauri-drag-region>{view === 'billing' ? 'Биллинг API' : chat?.indexedCodex ? 'Чаты Codex' : (!chat || chat.access === 'owner') ? 'Мои чаты' : 'Доступные мне'}</span>{remote ? <ServerQuotas menuRef={quotaMenu} onOpenChange={setQuotasOpen}/> : <Quotas userId={w.user.id} menuRef={quotaMenu} onOpenChange={setQuotasOpen}/> }
      {isAdmin && <button className="alt-action admin-entry" onClick={() => setPanel('admin')}>Админка</button>}</div>
      {connectionError && <p className="notice connection-error" role="alert">{connectionError}</p>}
      {!remote && incoming && <Incoming key={incoming.requestId} request={incoming}/>}
      {view === 'billing' ? <Billing back={() => setView('chat')}/> : chat ? <Conversation key={chat.id} chat={chat} billing={() => setView('billing')} share={() => setPanel('share')}/> : <div className="empty empty-hero thread"><div className="empty-squircle" aria-hidden="true"><CodeXml size={26} strokeWidth={2.1}/></div><h2>Пока нет чатов</h2><p>Создайте первый диалог, чтобы начать работу с агентом.</p><button onClick={() => void newChat()}>Создать чат</button></div>}
      {panel === 'admin' && isAdmin && <AdminDialog admin={remote.admin} userId={w.user.id} close={() => setPanel(null)}/>}
      {panel && panel !== 'admin' && <WorkspaceDialog kind={panel} w={w} chatId={activeId} close={() => setPanel(null)}/>}
    </main></div>
}
