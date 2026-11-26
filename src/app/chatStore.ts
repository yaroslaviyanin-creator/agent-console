import { historyUntil, previousTurn } from './runHistory'
import { create } from 'zustand'
import type { LoginRequest, MessageAttachment, Workspace, PermissionDecision, Provider, RunEvent, ServerMessage, ServerModel, ShareAccess, Target, ToolCall } from '../protocol/types'
import { useAccountStore, blockReason } from './accountStore'
import { readConnection, saveConnection, setTransport, transport } from './runtime'
import type { ConnectionMode } from './runtime'
import { RemoteError, RemoteTransport, normalizeServerAddress } from '../transport/RemoteTransport'

export const providerLabels = { antigravity: 'Antigravity', codex: 'Codex', claude: 'Claude', paid_api: 'Paid API' } satisfies Record<Provider, string>
export const models = [
  { id: 'sonnet', provider: 'claude', label: 'Claude Sonnet', shortLabel: 'Sonnet' },
  { id: 'opus', provider: 'claude', label: 'Claude Opus', shortLabel: 'Opus' },
  { id: 'codex', provider: 'codex', label: 'Codex', shortLabel: 'По умолчанию' },
  { id: 'gpt-5.2-codex', provider: 'codex', label: 'GPT-5.2-Codex', shortLabel: 'GPT-5.2-Codex' },
  { id: 'deepseek', provider: 'paid_api', label: 'DeepSeek', shortLabel: 'DeepSeek' },
  { id: 'deepseek-reasoner', provider: 'paid_api', label: 'DeepSeek Reasoner', shortLabel: 'DeepSeek Reasoner' },
  { id: 'gemini-4-hydrogen-low', provider: 'antigravity', label: 'Gemini 4 Hydrogen (Low)', shortLabel: 'Gemini 4 Hydrogen', reasoning: 'Low' },
  { id: 'gemini-4-hydrogen-medium', provider: 'antigravity', label: 'Gemini 4 Hydrogen (Medium)', shortLabel: 'Gemini 4 Hydrogen', reasoning: 'Medium' },
  { id: 'gemini-4-hydrogen-high', provider: 'antigravity', label: 'Gemini 4 Hydrogen (High)', shortLabel: 'Gemini 4 Hydrogen', reasoning: 'High' },
  { id: 'gemini-4-hydrogen-xhigh', provider: 'antigravity', label: 'Gemini 4 Hydrogen (XHigh)', shortLabel: 'Gemini 4 Hydrogen', reasoning: 'XHigh' },
  { id: 'gemini-3.8-flash-low', provider: 'antigravity', label: 'Gemini 3.8 Flash (Low)', shortLabel: 'Gemini 3.8 Flash', reasoning: 'Low' },
  { id: 'gemini-3.8-flash-medium', provider: 'antigravity', label: 'Gemini 3.8 Flash (Medium)', shortLabel: 'Gemini 3.8 Flash', reasoning: 'Medium' },
  { id: 'gemini-3.8-flash-high', provider: 'antigravity', label: 'Gemini 3.8 Flash (High)', shortLabel: 'Gemini 3.8 Flash', reasoning: 'High' },
  { id: 'claude-sonnet-4-6', provider: 'antigravity', label: 'Claude Sonnet 4.6 (Thinking)', shortLabel: 'Claude Sonnet 4.6', reasoning: 'Thinking' },
  { id: 'claude-opus-4-6-thinking', provider: 'antigravity', label: 'Claude Opus 4.6 (Thinking)', shortLabel: 'Claude Opus 4.6', reasoning: 'Thinking' },
  { id: 'gpt-oss-120b-medium', provider: 'antigravity', label: 'GPT-OSS 120B (Medium)', shortLabel: 'GPT-OSS 120B', reasoning: 'Medium' },
] as const satisfies ReadonlyArray<{ id: string; provider: Provider; label: string; shortLabel: string; reasoning?: string }>
export const demoPrompt = 'Увеличь тайм-аут подключения в settings.ts с 1 до 3 секунд.'
export interface ToolEntry {
  kind: 'tool'
  callId: string
  tool: ToolCall
  permission?: Extract<RunEvent, { type: 'permission_request' }>
  decision?: PermissionDecision
  sending?: boolean
  problem?: string
  result?: Extract<RunEvent, { type: 'tool_result' }>
}
export type Part = { kind: 'thinking' | 'text'; text: string } | ToolEntry
export interface Turn {
  attachments?: MessageAttachment[]
  progress?: string
  durationMs?: number
  id: string
  prompt: string
  parentId?: string | null
  retryOf?: string
  target?: Target
  refusalReason?: string
  authorId?: string
  time: number
  model: string
  parts: Part[]
  seq: number
  state: 'running' | 'completed' | 'stopped' | 'error' | 'refused' | 'denied'
  notice?: string
}
export interface Chat {
  id: string
  title: string
  access: ShareAccess | 'owner'
  model: string
  reasoning?: string | undefined
  target: Target
  draft: string
  turns: Turn[]
  indexedCodex?: boolean
  archived?: boolean
  historyLoaded?: boolean
  historyError?: string | undefined
}
function chat(id: string, title: string, access: Chat['access'] = 'owner'): Chat {
  return { id, title, access, model: 'sonnet', target: { mode: 'chat' }, draft: '', turns: [] }
}
function example(id: string, title: string, access: Chat['access'], prompt: string, reply: string): Chat {
  return { ...chat(id, title, access), turns: [{
    id: 'sample-' + id, prompt, time: Date.now() - 3600000, model: 'Claude Sonnet',
    seq: 1, state: 'completed', parts: [{ kind: 'text', text: reply }],
  }] }
}
const first = { ...chat('timeout', 'Тайм-аут подключения'), target: { mode: 'mcp', deviceId: 'work' } as const }
const initial = [
  first,
  example('review', 'Обзор проекта', 'owner', 'Как устроен клиент?', 'Интерфейс общается с агентами через **TransportAdapter**. События потока описаны в `src/protocol/types.ts`.'),
  example('tests', 'Проверка тестов', 'owner', 'Как запустить тесты?', 'Из корня проекта выполните:\n\n```sh\nnpm test\n```'),
  example('shared', 'Архитектура API', 'read', 'Где работают агенты?', 'CLI-агенты работают на удалённом сервере. Клиент получает унифицированные события.'),
  example('branch', 'План рефакторинга', 'fork', 'С чего начать?', '1. Уточнить границы модуля.\n2. Проверить существующие тесты.\n3. Внести изменение.'),
  example('team', 'Командный чат', 'write', 'Для чего этот чат?', 'Здесь участники с полным доступом могут отправлять сообщения.'),
]
const savedConnection = readConnection()
export const useChats = create<{
  activeId: string
  chats: Chat[]
  workspace: Workspace | null
  mode: ConnectionMode
  address: string
  remote: RemoteTransport | null
  restoring: boolean
  connected: boolean
  connectionError: string
  serverModels: ServerModel[]
  modelsLoading: boolean
  modelsError: string
  codexIndex: 'hidden' | 'loading' | 'ready' | 'error'
}>(() => ({
  activeId: '', chats: [], workspace: null, mode: savedConnection.mode, address: savedConnection.address,
  remote: null, restoring: false, connected: false, connectionError: '', serverModels: [], modelsLoading: false, codexIndex: 'hidden', modelsError: '',
}))

// The mock server and transcripts live for this app launch. Drafts belong to the signed-in user.
const transcripts = new Map(initial.map(c => [c.id, c]))
const drafts = new Map<string, string>()
export function availableDevices(w: Workspace, chatId: string) {
  const room = w.chats.find(c => c.id === chatId)
  return w.devices.filter(d => d.ownerId === w.user.id || d.id === room?.trustedDeviceId)
}
function remember() {
  const { workspace, chats } = useChats.getState()
  if (!workspace) return
  for (const c of chats) {
    transcripts.set(c.id, { ...c, draft: '' })
    drafts.set(workspace.user.id + '/' + c.id, c.draft)
  }
}
function loadWorkspace(w: Workspace | null, preferred?: string) {
  remember()
  if (!w) { useChats.setState({ workspace: null, chats: [], activeId: '' }); return }
  const chats = w.chats.map(room => {
    const saved = transcripts.get(room.id) ?? chat(room.id, 'Новый чат')
    let target = saved.target
    if (target.mode === 'mcp') {
      const deviceId = target.deviceId
      if (!availableDevices(w, room.id).some(d => d.id === deviceId && d.online)) {
        target = deviceId === 'work' ? { mode: 'mcp', deviceId: w.deviceId } : { mode: 'chat' }
      }
    }
    const turns = saved.turns.map(t => t.authorId ? t : { ...t, authorId: room.ownerId })
    return { ...saved, turns, access: room.access, target, draft: drafts.get(w.user.id + '/' + room.id) ?? '' }
  })
  const id = preferred ?? useChats.getState().activeId
  useChats.setState({ workspace: w, chats, activeId: chats.some(c => c.id === id) ? id : chats[0]?.id ?? '' })
}
export function updateSelfProfileMember(patch: { displayName?: string; avatarData?: string }) {
  useChats.setState(state => {
    const w = state.workspace
    if (!w) return state
    const nextUser = {
      ...w.user,
      ...(patch.displayName !== undefined ? { displayName: patch.displayName } : {}),
      ...(patch.avatarData !== undefined ? { avatarData: patch.avatarData } : {}),
    }
    return {
      workspace: {
        ...w,
        user: nextUser,
        users: w.users.map(u => (u.id === w.user.id ? { ...u, ...nextUser } : u)),
      },
    }
  })
}

export function setConnectionMode(mode: ConnectionMode) {
  if (mode === useChats.getState().mode) return
  logout()
  const { address } = useChats.getState()
  useChats.setState({ mode, connectionError: '' })
  saveConnection({ mode, address })
}

export function changeServerAddress(value: string) {
  const address = normalizeServerAddress(value)
  if (address === useChats.getState().address) return
  logout()
  useChats.setState({ mode: 'server', address, connectionError: '' })
  saveConnection({ mode: 'server', address })
}

export async function login(request: LoginRequest, register = false) {
  const { mode } = useChats.getState()
  if (mode === 'demo') {
    loadWorkspace(await transport.workspace.login(request))
    return
  }
  const address = normalizeServerAddress(request.connection)
  useChats.setState({ address, connectionError: '' })
  saveConnection({ mode, address })
  const remote = new RemoteTransport(address)
  try {
    attachRemote(remote, await (register ? remote.register(request) : remote.workspace.login(request)))
  } catch (error) {
    void remote.workspace.logout()
    throw error
  }
}

export async function restoreConnection() {
  const { mode, address, token } = readConnection()
  if (mode !== 'server' || !token || useChats.getState().restoring || useChats.getState().workspace) return
  useChats.setState({ restoring: true, connectionError: '' })
  let remote: RemoteTransport | undefined
  try {
    remote = new RemoteTransport(address)
    attachRemote(remote, await remote.restore(token))
  } catch (error) {
    remote?.close()
    if (error instanceof RemoteError && error.sessionExpired) saveConnection({ mode, address })
    useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось восстановить сессию.' })
  } finally {
    useChats.setState({ restoring: false })
  }
}

export function logout() {
  void transport.workspace.logout()
  clearSession()
}

function clearSession() {
  useAccountStore.setState({ serverQuota: null })
  const { remote, mode, address } = useChats.getState()
  for (const id of runs.keys()) stopChat(id)
  if (remote) {
    setTransport()
    useChats.setState({ remote: null, workspace: null, chats: [], activeId: '', connected: false, connectionError: '', serverModels: [], modelsLoading: false, codexIndex: 'hidden', modelsError: '' })
  } else {
    loadWorkspace(null)
  }
  if (mode === 'server') saveConnection({ mode, address })
}

function attachRemote(remote: RemoteTransport, workspace: Workspace) {
  setTransport(remote)
  saveConnection({ mode: 'server', address: remote.address, token: remote.sessionToken })
  useChats.setState({ remote, address: remote.address, connected: false, connectionError: '' })
  useAccountStore.setState({ serverQuota: remote.sessionQuota })
  applyRemoteWorkspace(remote, workspace)
  remote.subscribe(change => {
    if (useChats.getState().remote !== remote) return
    switch (change.type) {
      case 'connection':
        useChats.setState({ connected: change.connected })
        if (change.connected) {
          void refreshRemote(remote)
          void refreshQuota(remote)
        } else for (const c of useChats.getState().chats) {
          if (!runs.has(c.id) && c.turns.some(t => t.state === 'running')) stopChat(c.id)
        }
        break
      case 'run':
        receiveRemoteRun(remote, change.event)
        break
      case 'workspace_changed':
        void refreshRemote(remote)
        break
      case 'settled':
        void refreshQuota(remote)
        void refreshRemote(remote, change.chatId)
        break
      case 'pending':
        void checkAccountStatus().catch(error => {
          if (useChats.getState().remote === remote) {
            useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось проверить статус.' })
          }
        })
        break
      case 'expired':
        remote.close()
        clearSession()
        useChats.setState({ connectionError: 'Сессия закончилась. Войдите снова.' })
    }
  })
  if (workspace.user.role !== 'pending') startRemote(remote)
}

export async function checkAccountStatus() {
  const { remote } = useChats.getState()
  if (!remote) return
  const workspace = await remote.checkStatus()
  if (useChats.getState().remote !== remote) return
  useAccountStore.setState({ serverQuota: remote.sessionQuota })
  applyRemoteWorkspace(remote, workspace)
  useChats.setState({ connectionError: '' })
  if (workspace.user.role !== 'pending') startRemote(remote)
  else useChats.setState({ connected: false, serverModels: [], codexIndex: 'hidden' })
}

function startRemote(remote: RemoteTransport) {
  void remote.connect()
  if (remote.provider === 'codex') {
    void refreshServerModels()
    void refreshCodexIndex()
  } else if (remote.provider === 'antigravity') {
    void refreshServerModels()
  }
}

export async function refreshServerModels() {
  const { remote, modelsLoading } = useChats.getState()
  if (!remote || (remote.provider !== 'codex' && remote.provider !== 'antigravity') || modelsLoading || useChats.getState().workspace?.user.role === 'pending') return
  useChats.setState({ modelsLoading: true, modelsError: '' })
  try {
    const serverModels = await remote.models()
    if (useChats.getState().remote === remote) {
      useChats.setState({ serverModels, modelsError: serverModels.length ? '' : (remote.provider === 'antigravity' ? 'Antigravity не вернул доступных моделей.' : 'Codex не вернул доступных моделей.') })
    }
  } catch (error) {
    if (useChats.getState().remote === remote) {
      useChats.setState({ modelsError: error instanceof RemoteError && error.status === 404
        ? 'Обновите сервер, чтобы загрузить список моделей.'
        : (remote.provider === 'antigravity' ? 'Не удалось загрузить список моделей Antigravity.' : 'Не удалось загрузить список моделей Codex.') })
    }
  } finally {
    if (useChats.getState().remote === remote) useChats.setState({ modelsLoading: false })
  }
}

export async function refreshCodexIndex() {
  const { remote, codexIndex } = useChats.getState()
  if (remote?.provider !== 'codex' || codexIndex === 'loading' || useChats.getState().workspace?.user.role === 'pending') return
  useChats.setState({ codexIndex: 'loading' })
  try {
    const workspace = await remote.refreshCodexIndex()
    if (useChats.getState().remote !== remote) return
    if (workspace) applyRemoteWorkspace(remote, workspace)
    useChats.setState({ codexIndex: workspace ? 'ready' : 'hidden' })
  } catch {
    if (useChats.getState().remote === remote) useChats.setState({ codexIndex: 'error' })
  }
}

function applyRemoteWorkspace(remote: RemoteTransport, workspace: Workspace, preferred?: string) {
  const state = useChats.getState()
  if (state.remote !== remote) return
  const existing = new Map(state.chats.map(item => [item.id, item]))
  const accessById = new Map(workspace.chats.map(c => [c.id, c.access]))
  const chats = remote.chats.map(item => {
    const previous = existing.get(item.id)
    const model = remote.provider === 'stub' || item.model === 'codex' || item.model === 'antigravity' ? 'default' : item.model
    if ('archived' in item && typeof item.archived === 'boolean') {
      return { ...(previous ?? chat(item.id, item.title, 'read')), title: item.title, model, indexedCodex: true, archived: item.archived }
    }
    const access = accessById.get(item.id) ?? 'read'
    return previous ? { ...previous, title: item.title, access } : { ...chat(item.id, item.title, access), model }
  })
  for (const id of runs.keys()) if (!accessById.has(id)) stopChat(id)
  const selected = preferred ?? state.activeId
  const activeId = chats.some(c => c.id === selected) ? selected : chats[0]?.id ?? ''
  useChats.setState({ workspace, chats, activeId })
  if (activeId !== state.activeId && activeId) void loadRemoteHistory(remote, activeId)
}

function receiveRemoteRun(remote: RemoteTransport, event: RunEvent) {
  if (event.type !== 'started') {
    updateTurn(event.chatId, event.messageId, turn => applyEvent(turn, event))
    return
  }
  updateChat(event.chatId, c => {
    const previous = c.turns.find(t => t.id === event.messageId)
    if (previous && (previous.seq > 0 || previous.state === 'completed')) return c
    const label = useChats.getState().serverModels.find(m => m.id === event.model)?.name
    const turn: Turn = {
      id: event.messageId, prompt: event.prompt, time: event.createdAt, parts: [], seq: event.seq,
      model: remote.provider === 'stub' ? 'StubProvider' : label ?? 'Codex', state: 'running',
    }
    const turns = previous ? c.turns.map(t => t.id === turn.id ? turn : t) : [...c.turns, turn]
    return { ...c, turns }
  })
}

const quotaRequests = new WeakMap<RemoteTransport, AbortController>()
async function refreshQuota(remote: RemoteTransport) {
  quotaRequests.get(remote)?.abort()
  const controller = new AbortController()
  quotaRequests.set(remote, controller)
  try {
    const serverQuota = await remote.quota(controller.signal)
    const { remote: active, workspace } = useChats.getState()
    if (!controller.signal.aborted && active === remote && workspace?.user.role !== 'pending') {
      useAccountStore.setState({ serverQuota })
    }
  } catch { /* A later refresh can retry while the last successful reading stays visible. */ }
}

async function refreshRemote(remote: RemoteTransport, changedId?: string) {
  try {
    const workspace = await remote.refreshChats()
    if (useChats.getState().remote !== remote) return
    applyRemoteWorkspace(remote, workspace)
    const { activeId, connectionError } = useChats.getState()
    if (connectionError) useChats.setState({ connectionError: '' })
    if (activeId) void loadRemoteHistory(remote, activeId)
    if (changedId && changedId !== activeId && useChats.getState().chats.some(c => c.id === changedId && c.historyLoaded)) {
      void loadRemoteHistory(remote, changedId)
    }
  } catch (error) {
    if (useChats.getState().remote === remote) {
      useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось обновить список чатов.' })
    }
  }
}

const historyRequests = new WeakMap<RemoteTransport, Map<string, AbortController>>()
async function loadRemoteHistory(remote: RemoteTransport, id: string) {
  const pending = historyRequests.get(remote) ?? new Map<string, AbortController>()
  historyRequests.set(remote, pending)
  pending.get(id)?.abort()
  const controller = new AbortController()
  pending.set(id, controller)
  updateChat(id, c => ({ ...c, historyError: undefined }))
  try {
    const messages = await remote.messages(id, controller.signal)
    if (controller.signal.aborted || useChats.getState().remote !== remote) return
    updateChat(id, c => ({ ...c, historyLoaded: true, turns: restoreTurns(messages, c.turns, remote.provider === 'stub' ? 'StubProvider' : providerLabels[remote.provider]) }))
  } catch (error) {
    if (!controller.signal.aborted && useChats.getState().remote === remote) {
      updateChat(id, c => ({ ...c, historyError: error instanceof Error ? error.message : 'Не удалось загрузить историю.' }))
    }
  } finally {
    if (pending.get(id) === controller) pending.delete(id)
  }
}

function restoreTurns(messages: ServerMessage[], local: Turn[], label: string): Turn[] {
  const turns = new Map<string, Turn>()
  const localByID = new Map(local.map(turn => [turn.id, turn]))
  let previousPrompt: Turn | undefined
  for (const message of messages) {
    if (message.role === 'user') {
      const id = message.runId ?? message.id
      const pending = localByID.get(id)
      previousPrompt = pending ?? {
        id, prompt: message.content, model: label, time: message.createdAt, parts: [], seq: 0,
        ...(message.attachments?.length ? { attachments: message.attachments } : {}),
        state: 'stopped', notice: 'В истории пока есть только запрос.',
      }
      turns.set(id, previousPrompt)
    } else {
      const prompt = turns.get(message.runId ?? message.id) ?? (!message.runId ? previousPrompt : undefined)
      if (prompt && prompt.id !== message.id) turns.delete(prompt.id)
      const parts: Part[] = []
      if (message.thinking) parts.push({ kind: 'thinking', text: message.thinking })
      if (message.content) parts.push({ kind: 'text', text: message.content })
      turns.set(message.id, {
        id: message.id, prompt: prompt?.prompt ?? '', model: localByID.get(message.id)?.model ?? label,
        time: prompt?.time ?? message.createdAt, parts, seq: 0,
        ...(prompt?.attachments?.length ? { attachments: prompt.attachments } : {}),
        ...(message.progress ? { progress: message.progress } : {}),
        ...(message.durationMs != null ? { durationMs: message.durationMs } : {}),
        state: 'completed',
      })
      previousPrompt = undefined
    }
  }
  for (const turn of local) if (!turns.has(turn.id)) turns.set(turn.id, turn)
  return [...turns.values()]
}

export function retryHistory() {
  const { remote, activeId } = useChats.getState()
  if (remote && activeId) void loadRemoteHistory(remote, activeId)
}
export async function refreshServerDevices() {
  const { remote } = useChats.getState()
  if (remote) applyRemoteWorkspace(remote, await remote.refreshDevices())
}

export async function revokeDevice(id: string) {
  const { remote } = useChats.getState()
  if (remote) {
    const workspace = await remote.workspace.revokeDevice(id)
    if (useChats.getState().remote !== remote) return
    if (workspace) applyRemoteWorkspace(remote, workspace)
    else clearSession()
    return
  }
  const workspace = await transport.workspace.revokeDevice(id)
  for (const key of runs.keys()) {
    const target = useChats.getState().chats.find(c => c.id === key)?.target
    if (!workspace || (target?.mode === 'mcp' && target.deviceId === id)) stopChat(key)
  }
  loadWorkspace(workspace)
}
export async function setShare(chatId: string, userId: string, access: ShareAccess | null, trust: boolean) {
  const { remote } = useChats.getState()
  const workspace = await transport.workspace.setShare(chatId, userId, access, trust)
  if (remote) applyRemoteWorkspace(remote, workspace)
  else loadWorkspace(workspace)
}

export function updateChat(id: string, transform: (value: Chat) => Chat) {
  useChats.setState(state => ({ chats: state.chats.map(c => c.id === id ? transform(c) : c) }))
}
const runs = new Map<string, AbortController>()
function updateTurn(id: string, turnId: string, transform: (value: Turn) => Turn) {
  updateChat(id, c => ({ ...c, turns: c.turns.map(t => t.id === turnId ? transform(t) : t) }))
}
export function applyEvent(turn: Turn, event: RunEvent): Turn {
  if (event.messageId !== turn.id || event.seq <= turn.seq || turn.state !== 'running') return turn
  const next = { ...turn, seq: event.seq }
  switch (event.type) {
    case 'text_delta':
    case 'thinking_delta': {
      const kind = event.type === 'text_delta' ? 'text' : 'thinking'
      const tail = turn.parts.at(-1)
      next.parts = tail?.kind === kind
        ? [...turn.parts.slice(0, -1), { kind, text: tail.text + event.delta }]
        : [...turn.parts, { kind, text: event.delta }]
      break
    }
    case 'tool_use':
      next.parts = [...turn.parts, { kind: 'tool', tool: event.tool, callId: event.callId }]
      break
    case 'permission_request':
    case 'tool_result':
      next.parts = turn.parts.map(p => p.kind === 'tool' && p.callId === event.callId
        ? { ...p, ...(event.type === 'permission_request' ? { permission: event } : { result: event }) } : p)
      break
    case 'refusal_detected': next.refusalReason = event.reason; break
    case 'error': next.state = 'error'; next.notice = event.message; break
    case 'done':
      next.state = event.reason
      if (event.durationMs != null) next.durationMs = event.durationMs
      break
  }
  return next
}
export function stopChat(id: string) {
  runs.get(id)?.abort()
  runs.delete(id)
  updateChat(id, c => ({ ...c, turns: c.turns.map(t => t.state === 'running' ? { ...t, state: 'stopped' } : t) }))
}
export function selectChat(id: string) {
  const { activeId: active, remote } = useChats.getState()
  if (active !== id) {
    if (!remote && runs.has(active)) stopChat(active)
    useChats.setState({ activeId: id })
    if (remote) void loadRemoteHistory(remote, id)
  }
}
let creatingIn: RemoteTransport | null = null
export async function newChat(source?: Chat) {
  const { remote } = useChats.getState()
  if (remote) {
    if (creatingIn === remote) return
    creatingIn = remote
    useChats.setState({ connectionError: '' })
    try {
      const { id, workspace } = await remote.workspace.createChat(source?.id)
      applyRemoteWorkspace(remote, workspace, id)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось создать чат.' })
      }
    } finally {
      if (creatingIn === remote) creatingIn = null
    }
    return
  }
  const { id, workspace } = await transport.workspace.createChat(source?.id)
  const c = chat(id, source ? source.title + ' · копия' : 'Новый чат')
  if (source) Object.assign(c, { turns: structuredClone(source.turns), model: source.model, target: source.target })
  if (source) c.target = { mode: 'chat' }
  transcripts.set(id, c)
  const active = useChats.getState().activeId
  if (runs.has(active)) stopChat(active)
  loadWorkspace(workspace, id)
}
export function sendPrompt(id: string, input: string) {
  return startRun(id, input)
}
export function retryAnswer(chatId: string, turnId: string, modelId: string) {
  return startRun(chatId, '', { turnId, modelId })
}
async function startRun(id: string, input: string, retry?: { turnId: string; modelId: string }) {
  const { workspace, chats, remote, connected, serverModels } = useChats.getState()
  const c = chats.find(item => item.id === id)
  if (remote && (!connected || !c?.historyLoaded || retry)) return
  if (!workspace || !c || runs.has(id) || (c.access !== 'owner' && c.access !== 'write')) return
  const index = retry ? c.turns.findIndex(t => t.id === retry.turnId) : -1
  const source = c.turns[index]
  const serverModel = serverModels.find(m => m.id === c.model)
  if (remote && c.model !== 'default' && !serverModel) return
  const remoteProvider: Provider = remote?.provider === 'stub' || !remote ? 'codex' : remote.provider
  const model = remote
    ? { id: c.model, provider: remoteProvider, label: serverModel?.name ?? providerLabels[remoteProvider] }
    : models.find(m => m.id === (retry?.modelId ?? c.model))
  if (!model || (retry && (!source || source.state === 'running' || source.model === model.label))) return
  const prompt = source?.prompt ?? input.trim()
  if (!prompt || (!remote && blockReason(useAccountStore.getState().quotas[model.provider]))) return
  const parentId = source ? previousTurn(c.turns, index) : c.turns.at(-1)?.id ?? null
  const history = remote ? undefined : historyUntil(c.turns, parentId)
  const target = source?.target ?? (source ? { mode: 'chat' } as const : c.target)
  const retryFields = source ? { retryOf: source.id } : {}
  const turn: Turn = { ...retryFields, parentId, target, id: crypto.randomUUID(), time: Date.now(), authorId: workspace.user.id,
    prompt, model: remote?.provider === 'stub' ? 'StubProvider' : model.label, seq: 0, state: 'running', parts: [] }
  const controller = new AbortController()
  runs.set(id, controller)
  updateChat(id, value => ({ ...value, title: !remote && value.title === 'Новый чат' ? prompt : value.title, draft: source ? value.draft : '', model: model.id, target, turns: [...value.turns, turn] }))
  try {
    for await (const event of transport.stream({ chatId: id, messageId: turn.id, model: { id: model.id, provider: model.provider }, ...(c.reasoning ? { reasoning: c.reasoning } : {}), target, prompt, ...(history ? { history } : {}), ...retryFields }, controller.signal)) {
      if (controller.signal.aborted) break
      if (event.chatId === id && event.messageId === turn.id) {
        updateTurn(id, turn.id, t => applyEvent(t, event))
        if (remote && event.type === 'error' && (event.code === 'chat_busy' || event.code === 'server_busy')) {
          updateChat(id, current => current.draft ? current : { ...current, draft: prompt })
        }
        if (event.type === 'done' || event.type === 'error') break
      }
    }
    updateTurn(id, turn.id, t => t.state === 'running' ? { ...t, state: 'error', notice: 'Поток завершился без подтверждения.' } : t)
  } catch (error) {
    if (!controller.signal.aborted) updateTurn(id, turn.id, t => ({ ...t, state: 'error', notice: error instanceof Error ? error.message : 'Потеряно соединение' }))
  } finally {
    if (runs.get(id) === controller) runs.delete(id)
  }
}
export async function decide(id: string, turnId: string, entry: ToolEntry, decision: PermissionDecision) {
  if (!entry.permission || entry.sending || entry.decision || entry.result || !runs.has(id)) return
  function patch(values: Partial<ToolEntry>) {
    updateTurn(id, turnId, t => ({ ...t, parts: t.parts.map(p => p.kind === 'tool' && p.callId === entry.callId ? { ...p, ...values } : p) }))
  }
  patch({ sending: true, problem: '' })
  try {
    const accepted = await transport.respondToPermission(entry.permission.requestId, decision)
    patch(accepted ? { decision, sending: false } : { problem: 'Запрос разрешения уже закрыт.', sending: false })
  } catch {
    patch({ problem: 'Не удалось передать решение. Попробуйте снова.', sending: false })
  }
}
