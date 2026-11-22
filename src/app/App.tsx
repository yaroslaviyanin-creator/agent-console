import { useTelegramBack } from '../platform/telegram'
import { RetryActions } from './RetryActions'
import { Billing } from './Billing'
import { Incoming, LimitActions, Quotas } from './Quotas'
import { Login } from './Login'
import { WorkspaceDialog } from './WorkspaceDialog'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUp, Brain, Check, ChevronDown, ChevronRight, CodeXml, GitFork, Image as ImageIcon, Laptop, Menu, MessageSquare, PanelLeft, Paperclip, Settings2, Share2, Square, Terminal } from 'lucide-react'
import { transport } from './runtime'
import { blockReason, useAccountStore } from './accountStore'
import { demoPrompt, availableDevices, models, providerLabels, newChat, retryAnswer, retryHistory, sendPrompt, stopChat, updateChat, useChats } from './chatStore'
import type { Chat, Turn } from './chatStore'
import { ServerModelSelect } from './ServerModelSelect'
import { Sidebar } from './Sidebar'
import { ToolCard } from './ToolCard'
import { RichText } from './Markdown'

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
  const author = authorId === w.user.id ? 'Вы' : w.users.find(u => u.id === authorId)?.name ?? 'Участник'
  return <article className="turn">
    {(turn.prompt || !!turn.attachments?.length) && <>
      <div className="person"><span className="person-avatar">{author === 'Вы' ? 'Я' : author[0]}</span><strong>{author}</strong><time>{clock.format(turn.time)}</time></div>
      <div className="user-prompt">
        {!!turn.attachments?.length && <ul className="message-attachments" aria-label="Вложения">
          {turn.attachments.map((attachment, index) => {
            const Icon = attachment.kind === 'image' ? ImageIcon : Paperclip
            return <li key={index} title={attachment.name}><Icon size={14} aria-hidden="true"/><span>{attachment.name}</span></li>
          })}
        </ul>}
        {turn.prompt}
      </div>
    </>}
    <div className="person agent"><span className="agent-avatar"><CodeXml size={17}/></span><strong>{turn.model}</strong>{turn.retryOf && <span className="retry-label">Повтор ответа</span>}<time>{clock.format(turn.time)}</time></div>
    <div className="response">
      {turn.progress ? <details className="response-progress">
        <summary>{progressLabel(turn.durationMs)}<ChevronRight size={14}/></summary>
        <div className="response-progress-body"><RichText text={turn.progress}/></div>
      </details> : turn.durationMs != null && <span className="response-duration">{progressLabel(turn.durationMs)}</span>}
      {turn.parts.map((p, i) => p.kind === 'tool' ? <ToolCard key={p.callId} chatId={chatId} turn={turn} entry={p}/> : p.kind === 'thinking'
      ? <details className="thinking" key={i}><summary><Brain size={14}/>Thinking<ChevronRight size={13}/></summary><p>{p.text}</p></details>
      : <RichText text={p.text} key={i}/>)}
      {turn.parts.length === 0 && turn.state === 'running' && <span role="status" className="waiting">Агент подключается…</span>}
      {(turn.notice || turn.state === 'stopped') && <p role="status" className="notice">{turn.notice ?? 'Ответ остановлен.'}</p>}
      {demo && <RetryActions turn={turn} writable={writable} busy={busy} retry={retry}/>}
    </div>
  </article>
}

function Conversation({ chat, share, billing }: { chat: Chat; share: () => void; billing: () => void }) {
  const { workspace: w, remote, connected, serverModels } = useChats()
  const provider = models.find(m => m.id === chat.model)?.provider ?? (remote ? 'codex' : 'claude')
  const sampleBlocked = useAccountStore(s => !!blockReason(s.quotas.claude))
  const reason = useAccountStore(s => remote ? undefined : blockReason(s.quotas[provider]))
  const devices = availableDevices(w!, chat.id)
  const unavailableModel = !!remote && !chat.indexedCodex && chat.model !== 'default' && !serverModels.some(m => m.id === chat.model)
  const blocked = !!reason || !!remote && (!connected || !chat.historyLoaded || unavailableModel)
  const scroll = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const input = useRef<HTMLTextAreaElement>(null)
  const running = chat.turns.at(-1)?.state === 'running'
  const editable = chat.access === 'owner' || chat.access === 'write'
  const locked = running || !editable
  const last = chat.turns.at(-1)
  const pending = running && last?.parts.some(p => p.kind === 'tool' && p.permission && !p.result && !p.decision)
  useLayoutEffect(() => {
    const box = scroll.current
    if (box && follow.current) box.scrollTop = box.scrollHeight
  }, [chat.turns])
  const submit = () => {
    if (!blocked && !running && editable && chat.draft.trim()) {
      follow.current = true
      void sendPrompt(chat.id, chat.draft)
      input.current?.focus()
    }
  }
  return <>
    <header className="chat-heading"><div className="heading-inner"><div className="heading-icon"><Terminal size={21}/></div><div className="heading-text"><h1 title={chat.title}>{chat.title}</h1><div className="heading-meta"><i/>{remote ? 'Сервер' : 'Демо-сессия'}<span>/</span><span>{chat.indexedCodex ? 'История Codex' : chat.access === 'owner' ? 'Личный чат' : 'Общий чат'}</span></div></div>{!remote && chat.access === 'owner' && <button className="share-action" onClick={share} aria-label="Поделиться"><Share2 size={15}/><span>Поделиться</span></button>}</div><div className="header-controls">
          {remote ? <span className="server-provider">{remote.provider === 'codex' ? 'Codex' : 'StubProvider'}</span> : <label className="select-wrap catalog-select"><span>Провайдер</span><select aria-label="Провайдер" disabled={locked} value={provider} onChange={e => {
            const first = models.find(m => m.provider === e.currentTarget.value)
            if (first) updateChat(chat.id, c => ({ ...c, model: first.id }))
          }}>{Object.entries(providerLabels).map(([id, name]) => <option value={id} key={id}>{name}</option>)}</select><ChevronDown size={12}/></label>}
          {chat.indexedCodex ? null : remote ? <ServerModelSelect model={chat.model} unavailable={unavailableModel} disabled={locked}
            onChange={model => updateChat(chat.id, c => ({ ...c, model }))}/>
            : <label className="select-wrap catalog-select"><span>Модель</span><select aria-label="Модель" disabled={locked} value={chat.model} onChange={e => {
              const model = e.currentTarget.value
              updateChat(chat.id, c => ({ ...c, model }))
            }}>{models.filter(m => m.provider === provider).map(m => <option value={m.id} key={m.id}>{m.shortLabel}</option>)}</select><ChevronDown size={12}/></label>}
          {remote ? <span className="select-wrap"><Terminal size={14}/>Только чат</span> : <div className="select-wrap mode"><Terminal size={14}/><select aria-label="Режим" disabled={locked} value={chat.target.mode} onChange={e => updateChat(chat.id, c => ({ ...c, target: e.target.value === 'chat' ? { mode: 'chat' } : { mode: 'mcp', deviceId: w!.deviceId } }))}><option value="chat">Только чат</option><option value="mcp">Управление ПК (MCP)</option></select><ChevronDown size={12}/></div>}
          {chat.target.mode === 'mcp' && <label className="device-select"><Laptop size={13}/><select aria-label="Целевое устройство" disabled={locked} value={chat.target.deviceId} onChange={e => updateChat(chat.id, c => ({ ...c, target: { mode: 'mcp', deviceId: e.target.value } }))}>{devices.map(d => <option key={d.id} value={d.id} disabled={!d.online}>{d.name} · {d.ownerId !== w!.user.id ? 'ПК владельца · ' : ''}{d.online ? 'онлайн' : 'офлайн'}</option>)}</select><ChevronDown size={12}/></label>}</div></header>
    <div className="history" ref={scroll} onScroll={() => { const el = scroll.current; if (el) follow.current = el.scrollHeight - el.clientHeight - el.scrollTop < 48 }}>
      <div className="thread">
        {chat.historyError && <div className="notice" role="alert">{chat.historyError} <button className="text-action" onClick={retryHistory}>Повторить загрузку</button></div>}
        {remote && !chat.historyLoaded ? !chat.historyError && <p role="status" className="notice">Загружаю историю…</p>
          : chat.turns.length ? chat.turns.map(turn => <Transcript key={turn.id} chatId={chat.id} turn={turn} writable={editable} busy={running} demo={!remote} retry={modelId => {
        follow.current = true
        void retryAnswer(chat.id, turn.id, modelId)
      }}/>) :
        <div className="empty"><MessageSquare size={23}/><h2>{chat.indexedCodex ? 'В этом чате нет сообщений' : 'Начните разговор'}</h2>{remote ? !chat.indexedCodex && <p>Отправьте первое сообщение.</p> : editable && <><p>Отправьте сообщение или попробуйте демо-сценарий.</p><div>{['Предложи изменение settings.ts', 'Запусти тесты'].map(prompt => <button key={prompt} disabled={!!reason} onClick={() => { updateChat(chat.id, c => ({ ...c, target: { mode: 'mcp', deviceId: w!.deviceId } })); void sendPrompt(chat.id, prompt) }}>{prompt}<ChevronRight size={13}/></button>)}<button disabled={sampleBlocked} onClick={() => {
          updateChat(chat.id, c => ({ ...c, model: provider === 'claude' ? c.model : 'sonnet', target: { mode: 'chat' } }))
          void sendPrompt(chat.id, 'Объясни на примере, что такое тайм-аут подключения.')
        }}>Пример отказа модели<ChevronRight size={13}/></button></div></>}</div>}
      </div>
    </div>
    <footer className="composer-wrap"><div className="composer-inner">{editable ? <>
      {reason && !running && <LimitActions key={provider} w={w!} provider={provider} reason={reason} switchCodex={() => updateChat(chat.id,c => ({ ...c, model: 'codex' }))} billing={billing}/>}
      <div className="composer"><textarea disabled={!!reason} ref={input} rows={2} aria-label="Сообщение агенту" placeholder="Сообщение агенту…" value={chat.draft}
        onChange={e => updateChat(chat.id, c => ({ ...c, draft: e.target.value }))}
        onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); submit() } }}/>
        <div className="composer-controls">{chat.target.mode === 'mcp' && <label className="select-wrap mcp-examples"><Terminal size={14}/>
          <select aria-label="Сценарии MCP" disabled={!!reason || running} value="" onChange={e => {
            const draft = e.currentTarget.value
            if (draft) { updateChat(chat.id, c => ({ ...c, draft })); input.current?.focus() }
          }}><option value="" disabled>Сценарии MCP</option>{mcpExamples.map(([name, prompt]) => <option key={name} value={prompt}>{name}</option>)}</select><ChevronDown size={12}/></label>}
          <span className="key-hint"><kbd>Enter</kbd> отправить <span>·</span> <kbd>Shift ↵</kbd> новая строка</span>{running && !remote ? <button className="send stop" title="Остановить ответ" aria-label="Остановить ответ" onClick={() => stopChat(chat.id)}><Square size={14} fill="currentColor"/></button>
            : <button className="send" title="Отправить" aria-label="Отправить" disabled={blocked || running || !chat.draft.trim()} onClick={submit}><ArrowUp size={18}/></button>}</div></div>
      <div className="composer-meta"><span role="status">{pending ? 'Ожидается разрешение' : running ? 'Агент отвечает…' : <><Check size={13}/>{reason ? 'Ввод приостановлен' : remote && !connected ? 'Подключение к серверу…' : remote && !chat.historyLoaded ? 'Ожидание истории' : unavailableModel ? 'Выберите модель' : 'Готов к сообщению'}</>}</span></div>
    </> : <div className="access-note">{chat.access === 'fork' ? <button onClick={() => newChat(chat)}><GitFork size={15}/>Ответвить к себе</button> : 'Этот чат доступен для чтения'}</div>}</div></footer>
  </>
}
export function App() {
  const workspace = useChats(s => s.workspace)
  return workspace ? <Connected key={workspace.user.id}/> : <Login/>
}
function Connected() {
  const w = useChats(s => s.workspace)!
  const [view, setView] = useState<'chat' | 'billing'>('chat')
  const incoming = useAccountStore(s => s.requests.find(r => r.to.id === w.user.id && r.status === 'pending'))
  const [panel, setPanel] = useState<'share' | 'devices' | null>(null)
  const { chats, activeId, remote, connectionError } = useChats()
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
    <aside className="desktop-side"><Sidebar openBilling={() => setView('billing')} close={() => setView('chat')}/></aside>
    <dialog ref={drawer} onClose={() => setDrawerOpen(false)} className="drawer" aria-label="Список чатов" onClick={e => { if (e.target === drawer.current) drawer.current.close() }}><Sidebar openBilling={() => { drawer.current?.close(); setView('billing') }} close={() => { drawer.current?.close(); setView('chat') }}/></dialog>
    <main className="main"><div className="topbar"><button className="icon-btn desk-toggle" aria-label="Переключить боковую панель" onClick={() => setCollapsed(!collapsed)}><PanelLeft size={17}/></button><button className="icon-btn mobile" aria-label="Открыть список чатов" onClick={() => { drawer.current?.showModal(); setDrawerOpen(true) }}><Menu size={18}/></button>
      <span className="crumb">Рабочее пространство</span><ChevronRight className="crumb-arrow" size={13}/><span>{view === 'billing' ? 'Биллинг API' : chat?.indexedCodex ? 'Чаты Codex' : (!chat || chat.access === 'owner') ? 'Мои чаты' : 'Доступные мне'}</span>{!remote && <Quotas userId={w.user.id} menuRef={quotaMenu} onOpenChange={setQuotasOpen}/>}<button className="account-button" title="Настройки и устройства" aria-label="Настройки и устройства" onClick={() => setPanel('devices')}><Settings2 size={15}/>{'@' + (remote ? w.user.name : w.user.id)}</button></div>
      {connectionError && <p className="notice connection-error" role="alert">{connectionError}</p>}
      {!remote && incoming && <Incoming key={incoming.requestId} request={incoming}/>}
      {view === 'billing' ? <Billing back={() => setView('chat')}/> : chat ? <Conversation key={chat.id} chat={chat} billing={() => setView('billing')} share={() => setPanel('share')}/> : <div className="empty thread"><h2>Пока нет чатов</h2><button onClick={() => void newChat()}>Создать чат</button></div>}
      {panel && <WorkspaceDialog kind={panel} w={w} chatId={activeId} close={() => setPanel(null)}/>}
    </main></div>
}
