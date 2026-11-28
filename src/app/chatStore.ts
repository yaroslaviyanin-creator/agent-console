import { historyUntil, previousTurn } from './runHistory'
import { create } from 'zustand'
import type { LoginRequest, MessageAttachment, MessageUsage, Workspace, PermissionDecision, Provider, RunEvent, RunRequest, ServerMessage, ServerModel, ServerProvider, ServerProviderInfo, ShareAccess, Target, ToolCall } from '../protocol/types'
import { MockWorkspace } from '../transport/MockWorkspace'
import { type DraftAttachment, inferDraftKind, isSafeRasterPreviewFile, MAX_ATTACHMENT_MB, MAX_CHAT_ATTACHMENTS, MAX_CHAT_TOTAL_ATTACHMENTS, MAX_IMAGE_MB } from './attachments'
import { useAccountStore, blockReason } from './accountStore'
import { applyServerUISettings, setAppearanceSyncHandler } from './appearanceStore'
import { readConnection, saveConnection, setTransport, transport } from './runtime'
import type { ConnectionMode } from './runtime'
import { RemoteError, RemoteTransport, fallbackProviders, normalizeServerAddress } from '../transport/RemoteTransport'

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
  userMessageId?: string
  prompt: string
  parentId?: string | null
  retryOf?: string
  target?: Target
  refusalReason?: string
  authorId?: string
  time: number
  model: string
  modelId?: string
  provider?: ServerProvider
  usage?: MessageUsage
  parts: Part[]
  seq: number
  state: 'running' | 'completed' | 'stopped' | 'error' | 'refused' | 'denied'
  notice?: string
}
export interface Chat {
  id: string
  title: string
  pinned?: boolean
  access: ShareAccess | 'owner'
  provider?: ServerProvider
  serverProvider?: ServerProvider
  model: string
  reasoning?: string | undefined
  modelByProvider?: Partial<Record<ServerProvider, { model: string; reasoning?: string | undefined }>>
  target: Target
  draft: string
  attachments?: DraftAttachment[] | undefined
  attachmentError?: string | undefined
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
  serverProviders: ServerProviderInfo[]
  defaultServerProvider: ServerProvider | null
  providersLoaded: boolean
  serverModelsByProvider: Partial<Record<ServerProvider, ServerModel[]>>
  modelsLoadingByProvider: Partial<Record<ServerProvider, boolean>>
  modelsErrorByProvider: Partial<Record<ServerProvider, string>>
  lastModelByProvider: Partial<Record<ServerProvider, { model: string; reasoning?: string | undefined }>>
  codexIndex: 'hidden' | 'loading' | 'ready' | 'error'
}>(() => ({
  activeId: '', chats: [], workspace: null, mode: savedConnection.mode, address: savedConnection.address,
  remote: null, restoring: false, connected: false, connectionError: '',
  serverProviders: [], defaultServerProvider: null, providersLoaded: false,
  serverModelsByProvider: {}, modelsLoadingByProvider: {}, modelsErrorByProvider: {}, lastModelByProvider: {},
  codexIndex: 'hidden',
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
function sortChatsPinnedFirst(list: Chat[]): Chat[] {
  const pinned: Chat[] = []
  const regular: Chat[] = []
  for (const item of list) {
    if (item.pinned) pinned.push(item)
    else regular.push(item)
  }
  return [...pinned, ...regular]
}

export function resolveAvailableProviders(
  isRemote: boolean,
  serverProviders: readonly ServerProviderInfo[],
  defaultServerProvider: ServerProvider | null,
): ServerProviderInfo[] {
  if (!isRemote) {
    return [
      { id: 'claude', available: true },
      { id: 'codex', available: true },
      { id: 'antigravity', available: true },
    ]
  }
  const available = serverProviders.filter(p => p.available)
  if (available.length > 0) return available
  return defaultServerProvider ? [{ id: defaultServerProvider, available: true }] : []
}

function loadWorkspace(w: Workspace | null, preferred?: string) {
  remember()
  if (!w) { useChats.setState({ workspace: null, chats: [], activeId: '' }); return }
  const rawChats = w.chats.map(room => {
    const saved = transcripts.get(room.id) ?? chat(room.id, 'Новый чат')
    let target = saved.target
    if (target.mode === 'mcp') {
      const deviceId = target.deviceId
      if (!availableDevices(w, room.id).some(d => d.id === deviceId && d.online)) {
        target = deviceId === 'work' ? { mode: 'mcp', deviceId: w.deviceId } : { mode: 'chat' }
      }
    }
    const turns = saved.turns.map(t => t.authorId ? t : { ...t, authorId: room.ownerId })
    return {
      ...saved,
      pinned: Boolean(room.pinned ?? saved.pinned),
      turns,
      access: room.access,
      target,
      draft: drafts.get(w.user.id + '/' + room.id) ?? '',
    }
  })
  const chats = sortChatsPinnedFirst(rawChats)
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
  setAppearanceSyncHandler(null)
  useAccountStore.setState({ serverQuota: null })
  const { remote, mode, address } = useChats.getState()
  for (const id of runs.keys()) stopChat(id)
  if (remote) {
    setTransport()
    useChats.setState({
      remote: null, workspace: null, chats: [], activeId: '', connected: false, connectionError: '',
      serverProviders: [], defaultServerProvider: null, providersLoaded: false,
      serverModelsByProvider: {}, modelsLoadingByProvider: {}, modelsErrorByProvider: {}, lastModelByProvider: {},
      codexIndex: 'hidden',
    })
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
  if (workspace.user.role !== 'pending') {
    startRemote(remote)
    setAppearanceSyncHandler(uiSettings => remote.updateProfile({ uiSettings }).then(() => undefined))
    void remote.getProfile().then(profile => {
      if (useChats.getState().remote === remote && profile.uiSettings) {
        applyServerUISettings(profile.uiSettings)
      }
    }).catch(() => {})
  }
}

export async function checkAccountStatus() {
  const { remote } = useChats.getState()
  if (!remote) return
  const workspace = await remote.checkStatus()
  if (useChats.getState().remote !== remote) return
  useAccountStore.setState({ serverQuota: remote.sessionQuota })
  applyRemoteWorkspace(remote, workspace)
  useChats.setState({ connectionError: '' })
  if (workspace.user.role !== 'pending') {
    startRemote(remote)
    setAppearanceSyncHandler(uiSettings => remote.updateProfile({ uiSettings }).then(() => undefined))
    void remote.getProfile().then(profile => {
      if (useChats.getState().remote === remote && profile.uiSettings) {
        applyServerUISettings(profile.uiSettings)
      }
    }).catch(() => {})
  }
  else useChats.setState({
    connected: false,
    serverProviders: [], defaultServerProvider: null, providersLoaded: false,
    serverModelsByProvider: {}, modelsLoadingByProvider: {}, modelsErrorByProvider: {},
    codexIndex: 'hidden',
  })
}

const modelLoadSeq = new WeakMap<RemoteTransport, Partial<Record<ServerProvider, number>>>()

function isServerProviderAvailable(state: ReturnType<typeof useChats.getState>, provider: ServerProvider) {
  if (!state.providersLoaded) return true
  return state.serverProviders.some(p => p.id === provider && p.available)
}

function ensureChatModels(remote: RemoteTransport, chatId: string) {
  const state = useChats.getState()
  if (state.remote !== remote || state.workspace?.user.role === 'pending') return
  const targetChat = state.chats.find(c => c.id === chatId)
  if (!targetChat || targetChat.indexedCodex) return
  const provider = targetChat.provider ?? state.defaultServerProvider ?? remote.provider
  if (provider === 'stub') return
  if (state.serverModelsByProvider[provider] !== undefined || state.modelsLoadingByProvider[provider]) return
  if (!isServerProviderAvailable(state, provider)) return
  void refreshServerModels(provider, false, remote)
}

function startRemote(remote: RemoteTransport) {
  void remote.connect()
  void refreshServerProviders(remote)
  if (remote.provider !== 'stub') {
    void refreshServerModels(remote.provider, false, remote)
  }
  if (remote.provider === 'codex') {
    void refreshCodexIndex()
  }
}

export async function refreshServerProviders(targetRemote = useChats.getState().remote) {
  if (!targetRemote || useChats.getState().workspace?.user.role === 'pending') return
  try {
    const res = await targetRemote.providers()
    if (useChats.getState().remote !== targetRemote) return
    useChats.setState({
      defaultServerProvider: res.defaultProvider,
      serverProviders: res.providers,
      providersLoaded: true,
    })
    const state = useChats.getState()
    const activeChat = state.chats.find(c => c.id === state.activeId)
    const toLoad = new Set<ServerProvider>()
    if (res.defaultProvider !== 'stub' && res.providers.some(p => p.id === res.defaultProvider && p.available)) {
      toLoad.add(res.defaultProvider)
    }
    const activeProvider = activeChat?.provider
    if (activeProvider && activeProvider !== 'stub' && res.providers.some(p => p.id === activeProvider && p.available)) {
      toLoad.add(activeProvider)
    }
    for (const p of toLoad) {
      if (state.serverModelsByProvider[p] === undefined && !state.modelsLoadingByProvider[p]) {
        void refreshServerModels(p, false, targetRemote)
      }
    }
    if (res.providers.some(p => p.id === 'codex' && p.available) && state.codexIndex === 'hidden') {
      void refreshCodexIndex()
    }
  } catch {
    if (useChats.getState().remote !== targetRemote) return
    const fallback = fallbackProviders(targetRemote.provider)
    useChats.setState(state => state.providersLoaded ? state : {
      defaultServerProvider: fallback.defaultProvider,
      serverProviders: fallback.providers,
      providersLoaded: true,
    })
  }
}

export function resolveModelDisplay(
  providerId: string | undefined,
  rawModelId: string | undefined,
  serverModelsByProvider: Partial<Record<ServerProvider, ServerModel[]>>,
  fallbackLabel?: string,
): { modelId?: string; modelLabel: string } {
  const pid = providerId as ServerProvider | undefined
  const catalog = pid ? (serverModelsByProvider[pid] ?? []) : []
  const trimmed = rawModelId?.trim() ?? ''
  const isDefault = !trimmed || trimmed === 'default' || (!!pid && trimmed.toLowerCase() === pid.toLowerCase())
  if (isDefault) {
    const def = catalog.find(m => m.isDefault) ?? catalog[0]
    if (def) return { modelId: def.id, modelLabel: def.name || def.id }
    const demoDef = pid ? models.find(m => m.provider === pid) : undefined
    if (demoDef) return { modelId: demoDef.id, modelLabel: demoDef.label }
    const provName = pid === 'stub' ? 'StubProvider' : (pid ? (providerLabels[pid as Provider] ?? pid) : undefined)
    return { ...(trimmed ? { modelId: trimmed } : {}), modelLabel: provName ?? fallbackLabel ?? 'По умолчанию' }
  }
  const serverMatch = catalog.find(m => m.id === trimmed)
  if (serverMatch) return { modelId: serverMatch.id, modelLabel: serverMatch.name || serverMatch.id }
  const demoMatch = models.find(m => m.id === trimmed)
  if (demoMatch) return { modelId: demoMatch.id, modelLabel: demoMatch.label }
  const cleanFallback = fallbackLabel && fallbackLabel !== 'default' && (!pid || fallbackLabel.toLowerCase() !== pid.toLowerCase())
    ? fallbackLabel
    : undefined
  return { modelId: trimmed, modelLabel: cleanFallback ?? trimmed }
}

export async function refreshServerModels(
  provider?: ServerProvider,
  force = true,
  targetRemote = useChats.getState().remote,
) {
  const state = useChats.getState()
  const remote = targetRemote
  if (!remote || state.workspace?.user.role === 'pending') return
  const activeChat = state.chats.find(c => c.id === state.activeId)
  const resolvedProvider = provider ?? activeChat?.provider ?? state.defaultServerProvider ?? remote.provider
  if (resolvedProvider === 'stub') return
  if (!force && state.serverModelsByProvider[resolvedProvider] !== undefined) return
  if (state.modelsLoadingByProvider[resolvedProvider] && !force) return

  const seqMap = modelLoadSeq.get(remote) ?? {}
  const seq = (seqMap[resolvedProvider] ?? 0) + 1
  seqMap[resolvedProvider] = seq
  modelLoadSeq.set(remote, seqMap)

  useChats.setState(s => ({
    modelsLoadingByProvider: { ...s.modelsLoadingByProvider, [resolvedProvider]: true },
    modelsErrorByProvider: { ...s.modelsErrorByProvider, [resolvedProvider]: '' },
  }))
  const label = providerLabels[resolvedProvider] ?? resolvedProvider
  try {
    const serverModels = await remote.models(resolvedProvider)
    if (useChats.getState().remote !== remote || modelLoadSeq.get(remote)?.[resolvedProvider] !== seq) return
    useChats.setState(s => {
      const nextCatalog = { ...s.serverModelsByProvider, [resolvedProvider]: serverModels }
      return {
        serverModelsByProvider: nextCatalog,
        modelsErrorByProvider: {
          ...s.modelsErrorByProvider,
          [resolvedProvider]: serverModels.length ? '' : `${label} не вернул доступных моделей.`,
        },
        chats: s.chats.map(c => ({
          ...c,
          turns: c.turns.map(t => {
            const tp = t.provider ?? c.provider
            if (tp !== resolvedProvider) return t
            const res = resolveModelDisplay(tp, t.modelId, nextCatalog, t.model)
            if (res.modelId === t.modelId && res.modelLabel === t.model) return t
            return { ...t, ...(res.modelId ? { modelId: res.modelId } : {}), model: res.modelLabel }
          }),
        })),
      }
    })
  } catch (error) {
    if (useChats.getState().remote !== remote || modelLoadSeq.get(remote)?.[resolvedProvider] !== seq) return
    useChats.setState(s => ({
      modelsErrorByProvider: {
        ...s.modelsErrorByProvider,
        [resolvedProvider]: error instanceof RemoteError && error.status === 404
          ? 'Обновите сервер, чтобы загрузить список моделей.'
          : `Не удалось загрузить список моделей ${label}.`,
      },
    }))
  } finally {
    if (useChats.getState().remote === remote && modelLoadSeq.get(remote)?.[resolvedProvider] === seq) {
      useChats.setState(s => ({
        modelsLoadingByProvider: { ...s.modelsLoadingByProvider, [resolvedProvider]: false },
      }))
    }
  }
}

export async function refreshCodexIndex() {
  const { remote, codexIndex, serverProviders, providersLoaded } = useChats.getState()
  const codexAvailable = remote?.provider === 'codex' || (providersLoaded && serverProviders.some(p => p.id === 'codex' && p.available))
  if (!remote || !codexAvailable || codexIndex === 'loading' || useChats.getState().workspace?.user.role === 'pending') return
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
  const pinnedById = new Map(workspace.chats.map(c => [c.id, Boolean(c.pinned)]))
  const rawChats = remote.chats.map(item => {
    const previous = existing.get(item.id)
    const serverProvider = item.provider ?? remote.provider
    const serverModel = serverProvider === 'stub' || item.model === 'codex' || item.model === 'antigravity' ? 'default' : item.model
    if ('archived' in item && typeof item.archived === 'boolean') {
      return { ...(previous ?? chat(item.id, item.title, 'read')), title: item.title, pinned: false, provider: serverProvider, serverProvider, model: serverModel, indexedCodex: true, archived: item.archived }
    }
    const access = accessById.get(item.id) ?? 'read'
    const pinned = item.pinned ?? pinnedById.get(item.id) ?? false
    if (!previous) {
      return { ...chat(item.id, item.title, access), pinned, provider: serverProvider, serverProvider, model: serverModel }
    }
    const prevServerProvider = previous.serverProvider ?? serverProvider
    const hasLocalProviderOverride = previous.provider != null && previous.provider !== prevServerProvider
    const serverChanged = serverProvider !== prevServerProvider
    if (hasLocalProviderOverride && !serverChanged) {
      return { ...previous, title: item.title, pinned, access, serverProvider }
    }
    const externalProviderChange = serverChanged && previous.provider !== serverProvider
    return {
      ...previous,
      title: item.title,
      pinned,
      access,
      provider: serverProvider,
      serverProvider,
      ...(externalProviderChange ? { model: serverModel, reasoning: undefined } : {}),
    }
  })
  const chats = sortChatsPinnedFirst(rawChats)
  for (const id of runs.keys()) if (!accessById.has(id)) stopChat(id)
  const selected = preferred ?? state.activeId
  const activeId = chats.some(c => c.id === selected) ? selected : chats[0]?.id ?? ''
  useChats.setState({ workspace, chats, activeId })
  if (activeId !== state.activeId && activeId) {
    void loadRemoteHistory(remote, activeId)
    ensureChatModels(remote, activeId)
  }
}

function receiveRemoteRun(remote: RemoteTransport, event: RunEvent) {
  if (event.type !== 'started') {
    updateTurn(event.chatId, event.messageId, turn => applyEvent(turn, event))
    return
  }
  updateChat(event.chatId, c => {
    const previous = c.turns.find(t => t.id === event.messageId)
    if (previous && (previous.seq > 0 || previous.state === 'completed')) return c
    const chatProvider = c.provider ?? remote.provider
    const catalog = useChats.getState().serverModelsByProvider[chatProvider] ?? []
    const label = catalog.find(m => m.id === event.model)?.name
    const fallbackLabel = chatProvider === 'stub' ? 'StubProvider' : providerLabels[chatProvider]
    const turn: Turn = {
      id: event.messageId, prompt: event.prompt, time: event.createdAt, parts: [], seq: event.seq,
      ...(event.attachments?.length ? { attachments: event.attachments } : {}),
      model: label ?? fallbackLabel, state: 'running',
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
    updateChat(id, c => {
      const chatProvider = c.provider ?? remote.provider
      const label = chatProvider === 'stub' ? 'StubProvider' : providerLabels[chatProvider]
      return { ...c, historyLoaded: true, turns: restoreTurns(messages, c.turns, label) }
    })
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
      previousPrompt = pending
        ? { ...pending, userMessageId: message.id, prompt: message.content }
        : {
          id, userMessageId: message.id, prompt: message.content, model: label, time: message.createdAt, parts: [], seq: 0,
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
      const catalogSnapshot = useChats.getState().serverModelsByProvider
      const resolved = resolveModelDisplay(message.provider, message.model, catalogSnapshot, localByID.get(message.id)?.model ?? label)
      turns.set(message.id, {
        id: message.id,
        ...(prompt?.userMessageId ? { userMessageId: prompt.userMessageId } : {}),
        prompt: prompt?.prompt ?? '',
        model: resolved.modelLabel,
        ...(resolved.modelId ? { modelId: resolved.modelId } : {}),
        ...(message.provider ? { provider: message.provider } : {}),
        ...(message.usage ? { usage: message.usage } : {}),
        time: prompt?.time ?? message.createdAt,
        parts,
        seq: 0,
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
    case 'done': {
      next.state = event.reason
      if (event.durationMs != null) next.durationMs = event.durationMs
      if (event.provider) next.provider = event.provider
      if (event.model) next.modelId = event.model
      const resolved = resolveModelDisplay(next.provider, next.modelId, useChats.getState().serverModelsByProvider, next.model)
      if (resolved.modelId) next.modelId = resolved.modelId
      next.model = resolved.modelLabel
      if (event.usage) next.usage = event.usage
      break
    }
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
    if (remote) {
      void loadRemoteHistory(remote, id)
      ensureChatModels(remote, id)
    }
  }
}
function resolveProviderModel(
  source: Chat | undefined,
  provider: ServerProvider,
  catalog: readonly ServerModel[] | undefined,
  lastByProvider: Partial<Record<ServerProvider, { model: string; reasoning?: string | undefined }>>,
): { model: string; reasoning?: string | undefined } {
  if (provider === 'stub') return { model: 'default' }
  const preferred = source?.modelByProvider?.[provider] ?? lastByProvider[provider]
  if (preferred) {
    if (preferred.model === 'default') return { model: 'default', reasoning: preferred.reasoning }
    if (!catalog || catalog.some(m => m.id === preferred.model)) {
      return { model: preferred.model, reasoning: preferred.reasoning }
    }
  }
  const defaultModel = catalog?.find(m => m.isDefault)?.id ?? catalog?.[0]?.id ?? 'default'
  return { model: defaultModel }
}

export function selectChatModel(chatId: string, model: string) {
  useChats.setState(s => {
    const target = s.chats.find(c => c.id === chatId)
    if (!target) return s
    const activeProvider = s.remote ? (target.provider ?? s.defaultServerProvider ?? s.remote.provider) : undefined
    const entry = { model, ...(target.reasoning ? { reasoning: target.reasoning } : {}) }
    return {
      ...(activeProvider ? { lastModelByProvider: { ...s.lastModelByProvider, [activeProvider]: entry } } : {}),
      chats: s.chats.map(c => c.id === chatId ? {
        ...c,
        model,
        ...(activeProvider ? { modelByProvider: { ...c.modelByProvider, [activeProvider]: entry } } : {}),
      } : c),
    }
  })
}

export function selectChatReasoning(chatId: string, reasoning: string) {
  const normalized = reasoning || undefined
  useChats.setState(s => {
    const target = s.chats.find(c => c.id === chatId)
    if (!target) return s
    const activeProvider = s.remote ? (target.provider ?? s.defaultServerProvider ?? s.remote.provider) : undefined
    const entry = { model: target.model, ...(normalized ? { reasoning: normalized } : {}) }
    return {
      ...(activeProvider ? { lastModelByProvider: { ...s.lastModelByProvider, [activeProvider]: entry } } : {}),
      chats: s.chats.map(c => c.id === chatId ? {
        ...c,
        reasoning: normalized,
        ...(activeProvider ? { modelByProvider: { ...c.modelByProvider, [activeProvider]: entry } } : {}),
      } : c),
    }
  })
}

export async function switchChatProvider(chatId: string, nextProvider: ServerProvider) {
  const state = useChats.getState()
  const { remote, chats, serverProviders, providersLoaded, serverModelsByProvider, lastModelByProvider } = state
  if (!remote) return
  const source = chats.find(c => c.id === chatId)
  if (!source || source.access !== 'owner' || source.indexedCodex) return
  if (source.turns.some(t => t.state === 'running')) return
  const currentProvider = source.provider ?? state.defaultServerProvider ?? remote.provider
  if (nextProvider === currentProvider || nextProvider === 'paid_api') return
  if (providersLoaded && !serverProviders.some(p => p.id === nextProvider && p.available)) return

  const savedCurrent = { model: source.model, ...(source.reasoning ? { reasoning: source.reasoning } : {}) }
  const existingCatalog = serverModelsByProvider[nextProvider]
  const immediate = resolveProviderModel(source, nextProvider, existingCatalog, lastModelByProvider)

  useChats.setState(s => ({
    connectionError: '',
    lastModelByProvider: { ...s.lastModelByProvider, [currentProvider]: savedCurrent },
    chats: s.chats.map(c => c.id === chatId ? {
      ...c,
      provider: nextProvider,
      serverProvider: c.serverProvider ?? currentProvider,
      model: immediate.model,
      reasoning: immediate.reasoning,
      modelByProvider: { ...c.modelByProvider, [currentProvider]: savedCurrent },
    } : c),
  }))

  if (nextProvider !== 'stub' && existingCatalog === undefined) {
    await refreshServerModels(nextProvider, false, remote)
    const latest = useChats.getState()
    if (latest.remote !== remote) return
    const updatedChat = latest.chats.find(c => c.id === chatId)
    if (!updatedChat || updatedChat.provider !== nextProvider) return
    const loadedCatalog = latest.serverModelsByProvider[nextProvider]
    if (loadedCatalog && (updatedChat.model === 'default' || !loadedCatalog.some(m => m.id === updatedChat.model))) {
      const resolved = resolveProviderModel(updatedChat, nextProvider, loadedCatalog, latest.lastModelByProvider)
      if (resolved.model !== updatedChat.model || resolved.reasoning !== updatedChat.reasoning) {
        updateChat(chatId, c => c.provider === nextProvider ? { ...c, model: resolved.model, reasoning: resolved.reasoning } : c)
      }
    }
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
export async function uploadChatFiles(chatId: string, fileList: FileList | File[]) {
  const { remote, chats } = useChats.getState()
  const files = Array.from(fileList)
  if (!files.length) return
  if (!remote) {
    updateChat(chatId, c => ({ ...c, attachmentError: 'Загрузка файлов доступна в режиме «Сервер».' }))
    return
  }
  const target = chats.find(c => c.id === chatId)
  if (!target || (target.access !== 'owner' && target.access !== 'write')) return

  const existing = target.attachments ?? []
  const sentCount = target.turns.reduce((sum, t) => sum + (t.attachments?.length ?? 0), 0)
  const remainingInChat = MAX_CHAT_TOTAL_ATTACHMENTS - sentCount - existing.length
  const availableSlots = Math.min(MAX_CHAT_ATTACHMENTS - existing.length, remainingInChat)
  if (availableSlots <= 0) {
    const msg = remainingInChat <= 0
      ? `В одном чате можно прикрепить не более ${MAX_CHAT_TOTAL_ATTACHMENTS} файлов.`
      : `Можно прикрепить не более ${MAX_CHAT_ATTACHMENTS} файлов к одному сообщению.`
    updateChat(chatId, c => ({ ...c, attachmentError: msg }))
    return
  }

  const validFiles: File[] = []
  let sizeError = ''
  for (const file of files) {
    const isImage = inferDraftKind(file) === 'image'
    const limitMb = isImage ? MAX_IMAGE_MB : MAX_ATTACHMENT_MB
    if (file.size > limitMb * 1024 * 1024) {
      sizeError = `Файл «${file.name}» превышает лимит ${limitMb} МБ.`
      continue
    }
    validFiles.push(file)
  }
  if (!validFiles.length) {
    if (sizeError) updateChat(chatId, c => ({ ...c, attachmentError: sizeError }))
    return
  }

  const batch = validFiles.slice(0, availableSlots)
  const overflow = validFiles.length > availableSlots
  const pendingItems: DraftAttachment[] = batch.map(file => ({
    localId: crypto.randomUUID(),
    name: file.name,
    sizeBytes: file.size,
    kind: inferDraftKind(file),
    status: 'uploading',
    progress: 0,
    ...(isSafeRasterPreviewFile(file) && typeof URL !== 'undefined' && typeof URL.createObjectURL === 'function'
      ? { previewUrl: URL.createObjectURL(file) }
      : {}),
  }))

  updateChat(chatId, c => ({
    ...c,
    attachmentError: sizeError || (overflow ? `Прикреплены первые ${batch.length} из ${validFiles.length} файлов (лимит ${MAX_CHAT_ATTACHMENTS} на сообщение, ${MAX_CHAT_TOTAL_ATTACHMENTS} на чат).` : undefined),
    attachments: [...(c.attachments ?? []), ...pendingItems],
  }))

  const pendingIds = new Set(pendingItems.map(item => item.localId))
  try {
    const uploaded = await remote.uploadAttachments(chatId, batch, {
      onProgress: percent => {
        updateChat(chatId, c => ({
          ...c,
          attachments: (c.attachments ?? []).map(item =>
            pendingIds.has(item.localId) && item.status === 'uploading'
              ? { ...item, progress: percent }
              : item,
          ),
        }))
      },
    })
    updateChat(chatId, c => {
      const next = (c.attachments ?? []).map(item => {
        if (!pendingIds.has(item.localId)) return item
        const idx = pendingItems.findIndex(p => p.localId === item.localId)
        const remoteAtt = uploaded[idx]
        if (!remoteAtt) {
          return { ...item, status: 'error' as const, error: 'Сервер не вернул метаданные файла.' }
        }
        return {
          ...item,
          name: remoteAtt.name,
          sizeBytes: remoteAtt.sizeBytes ?? item.sizeBytes,
          kind: (remoteAtt.kind === 'image' || remoteAtt.kind === 'pdf' || remoteAtt.kind === 'archive' || remoteAtt.kind === 'document' ? remoteAtt.kind : 'text') as DraftAttachment['kind'],
          status: 'ready' as const,
          progress: 100,
          ...(remoteAtt.warning ? { warning: remoteAtt.warning } : {}),
          remote: remoteAtt,
        }
      })
      return { ...c, attachments: next }
    })
  } catch (error) {
    const msg = error instanceof Error ? error.message : 'Не удалось загрузить файлы.'
    for (const item of pendingItems) {
      if (item.previewUrl && typeof URL !== 'undefined') URL.revokeObjectURL(item.previewUrl)
    }
    updateChat(chatId, c => ({
      ...c,
      attachmentError: msg,
      attachments: (c.attachments ?? []).filter(item => !pendingIds.has(item.localId)),
    }))
  }
}

export function removeChatAttachment(chatId: string, localId: string) {
  const { remote, chats } = useChats.getState()
  const target = chats.find(c => c.id === chatId)
  const item = target?.attachments?.find(a => a.localId === localId)
  if (item?.previewUrl && typeof URL !== 'undefined') URL.revokeObjectURL(item.previewUrl)
  updateChat(chatId, c => ({
    ...c,
    attachmentError: undefined,
    attachments: (c.attachments ?? []).filter(a => a.localId !== localId),
  }))
  if (remote && item?.remote?.id) {
    void remote.deleteAttachment(item.remote.id).catch(() => {})
  }
}

export function clearChatAttachmentError(chatId: string) {
  updateChat(chatId, c => ({ ...c, attachmentError: undefined }))
}

export function exportChatMarkdown(c: Chat): string {
  const lines: string[] = [`# ${c.title || 'Чат'}`, '']
  for (const turn of c.turns) {
    const timeStr = new Date(turn.time).toLocaleString('ru-RU')
    if (turn.prompt) {
      lines.push(`## Пользователь (${timeStr})`, '', turn.prompt, '')
    }
    const textParts = turn.parts.flatMap(p => (p.kind === 'text' ? [p.text] : [])).join('\n\n').trim()
    const thinkingParts = turn.parts.flatMap(p => (p.kind === 'thinking' ? [p.text] : [])).join('\n\n').trim()
    const meta = [turn.model, turn.provider].filter(Boolean).join(' · ')
    lines.push(`## Ассистент${meta ? ` — ${meta}` : ''}`, '')
    if (thinkingParts) {
      lines.push('<details>', '<summary>Размышления</summary>', '', thinkingParts, '', '</details>', '')
    }
    if (textParts) {
      lines.push(textParts, '')
    } else if (turn.notice) {
      lines.push(`_${turn.notice}_`, '')
    }
    lines.push('---', '')
  }
  return lines.join('\n').trimEnd() + '\n'
}

export function downloadChatMarkdown(c: Chat) {
  if (typeof document === 'undefined' || typeof Blob === 'undefined' || typeof URL === 'undefined') return
  const md = exportChatMarkdown(c)
  const blob = new Blob([md], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    const safeName = (c.title || 'chat').replace(/[\\/:*?"<>|]+/g, '-').trim() || 'chat'
    a.href = url
    a.download = `${safeName}.md`
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
}

export async function renameChat(chatId: string, title: string) {
  const trimmed = title.trim()
  if (!trimmed || trimmed.length > 200) return
  const { remote, chats } = useChats.getState()
  const target = chats.find(c => c.id === chatId)
  if (!target || target.access !== 'owner' || target.indexedCodex) return
  if (remote) {
    try {
      const { workspace } = await remote.patchChat(chatId, { title: trimmed })
      if (useChats.getState().remote === remote) applyRemoteWorkspace(remote, workspace)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось переименовать чат.' })
      }
    }
    return
  }
  updateChat(chatId, c => ({ ...c, title: trimmed }))
  const saved = transcripts.get(chatId)
  if (saved) transcripts.set(chatId, { ...saved, title: trimmed })
}

export async function togglePinChat(chatId: string) {
  const { remote, chats } = useChats.getState()
  const target = chats.find(c => c.id === chatId)
  if (!target || target.access !== 'owner' || target.indexedCodex) return
  const nextPinned = !target.pinned
  if (remote) {
    try {
      const { workspace } = await remote.patchChat(chatId, { pinned: nextPinned })
      if (useChats.getState().remote === remote) applyRemoteWorkspace(remote, workspace)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось закрепить чат.' })
      }
    }
    return
  }
  if (transport.workspace instanceof MockWorkspace) {
    loadWorkspace(transport.workspace.setPinned(chatId, nextPinned))
    return
  }
  useChats.setState(s => ({
    chats: sortChatsPinnedFirst(s.chats.map(c => c.id === chatId ? { ...c, pinned: nextPinned } : c)),
  }))
}

export async function forkChatAt(chatId: string, upToTurnId?: string) {
  const { remote, chats } = useChats.getState()
  const source = chats.find(c => c.id === chatId)
  if (!source) return
  if (remote) {
    if (creatingIn === remote) return
    creatingIn = remote
    useChats.setState({ connectionError: '' })
    try {
      const { id, workspace } = await remote.forkChat(chatId, upToTurnId)
      if (useChats.getState().remote === remote) applyRemoteWorkspace(remote, workspace, id)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось создать ветку чата.' })
      }
    } finally {
      if (creatingIn === remote) creatingIn = null
    }
    return
  }
  const { id, workspace } = await transport.workspace.createChat(source.id)
  const idx = upToTurnId ? source.turns.findIndex(t => t.id === upToTurnId) : source.turns.length - 1
  const slicedTurns = idx >= 0 ? structuredClone(source.turns.slice(0, idx + 1)) : structuredClone(source.turns)
  const c: Chat = { ...chat(id, source.title + ' · копия'), turns: slicedTurns, model: source.model, target: { mode: 'chat' } }
  transcripts.set(id, c)
  const active = useChats.getState().activeId
  if (runs.has(active)) stopChat(active)
  loadWorkspace(workspace, id)
}

export async function deleteChatItem(chatId: string) {
  const { remote, chats } = useChats.getState()
  const target = chats.find(c => c.id === chatId)
  if (!target || target.access !== 'owner' || target.indexedCodex) return
  stopChat(chatId)
  if (remote) {
    try {
      const workspace = await remote.deleteChat(chatId)
      if (useChats.getState().remote === remote) applyRemoteWorkspace(remote, workspace)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось удалить чат.' })
      }
    }
    return
  }
  transcripts.delete(chatId)
  if (transport.workspace instanceof MockWorkspace) {
    loadWorkspace(transport.workspace.deleteChat(chatId))
    return
  }
  useChats.setState(s => {
    const remaining = s.chats.filter(c => c.id !== chatId)
    const activeId = s.activeId === chatId ? remaining[0]?.id ?? '' : s.activeId
    return { chats: remaining, activeId }
  })
}

export async function deleteChatMessage(chatId: string, turnId: string) {
  const { remote, chats } = useChats.getState()
  const target = chats.find(c => c.id === chatId)
  if (!target || runs.has(chatId) || (target.access !== 'owner' && target.access !== 'write')) return
  const turn = target.turns.find(t => t.id === turnId)
  if (!turn) return
  if (remote) {
    try {
      await remote.deleteMessage(chatId, turn.id)
      await loadRemoteHistory(remote, chatId)
    } catch (error) {
      if (useChats.getState().remote === remote) {
        useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось удалить сообщение.' })
      }
    }
    return
  }
  updateChat(chatId, c => ({ ...c, turns: c.turns.filter(t => t.id !== turnId) }))
}

async function executeTurnStream(
  chatId: string,
  turnId: string,
  request: Omit<RunRequest, 'chatId' | 'messageId'>,
  restoreDraftOnBusy?: string,
) {
  const controller = new AbortController()
  runs.set(chatId, controller)
  try {
    for await (const event of transport.stream({ ...request, chatId, messageId: turnId }, controller.signal)) {
      if (controller.signal.aborted) break
      if (event.chatId === chatId && event.messageId === turnId) {
        updateTurn(chatId, turnId, t => applyEvent(t, event))
        if (restoreDraftOnBusy !== undefined && event.type === 'error' && (event.code === 'chat_busy' || event.code === 'server_busy')) {
          updateChat(chatId, current => current.draft ? current : { ...current, draft: restoreDraftOnBusy })
        }
        if (event.type === 'done' || event.type === 'error') break
      }
    }
    updateTurn(chatId, turnId, t => t.state === 'running' ? { ...t, state: 'error', notice: 'Поток завершился без подтверждения.' } : t)
  } catch (error) {
    if (!controller.signal.aborted) {
      updateTurn(chatId, turnId, t => ({ ...t, state: 'error', notice: error instanceof Error ? error.message : 'Потеряно соединение' }))
    }
  } finally {
    if (runs.get(chatId) === controller) runs.delete(chatId)
  }
}

export async function regenerateTurn(
  chatId: string,
  turnId: string,
  options?: { provider?: ServerProvider; model?: string },
) {
  const { workspace, chats, remote, connected, serverModelsByProvider, lastModelByProvider } = useChats.getState()
  const c = chats.find(item => item.id === chatId)
  if (!workspace || !c || runs.has(chatId) || (c.access !== 'owner' && c.access !== 'write')) return
  const index = c.turns.findIndex(t => t.id === turnId)
  const source = index >= 0 ? c.turns[index] : undefined
  if (!source || source.state === 'running') return

  if (!remote) {
    const modelId = options?.model ?? c.model
    const model = models.find(m => m.id === modelId) ?? models[0]
    if (!source.prompt || blockReason(useAccountStore.getState().quotas[model.provider])) return
    const parentId = previousTurn(c.turns, index)
    const history = historyUntil(c.turns, parentId)
    const turn: Turn = {
      parentId,
      target: source.target ?? c.target,
      id: crypto.randomUUID(),
      time: Date.now(),
      authorId: workspace.user.id,
      ...(source.attachments?.length ? { attachments: source.attachments } : {}),
      prompt: source.prompt,
      model: model.label,
      modelId: model.id,
      provider: model.provider,
      seq: 0,
      state: 'running',
      parts: [],
    }
    updateChat(chatId, value => ({ ...value, model: model.id, turns: [...value.turns.slice(0, index), turn] }))
    await executeTurnStream(chatId, turn.id, {
      model: { id: model.id, provider: model.provider },
      target: turn.target!,
      prompt: source.prompt,
      history,
    })
    return
  }

  if (!connected || !c.historyLoaded) return
  const targetProvider: ServerProvider = options?.provider ?? c.provider ?? remote.provider
  if (targetProvider !== 'stub' && serverModelsByProvider[targetProvider] === undefined) {
    await refreshServerModels(targetProvider, false, remote)
  }
  const latest = useChats.getState()
  const catalog = latest.serverModelsByProvider[targetProvider] ?? []
  const resolved = options?.model
    ? { model: options.model, reasoning: c.reasoning }
    : targetProvider === (c.provider ?? remote.provider)
      ? { model: c.model, reasoning: c.reasoning }
      : resolveProviderModel(c, targetProvider, catalog, latest.lastModelByProvider ?? lastModelByProvider)

  try {
    const prep = await remote.regenerateMessage(chatId, source.id, { provider: targetProvider, model: resolved.model })
    const resolvedDisplay = resolveModelDisplay(targetProvider, resolved.model, latest.serverModelsByProvider)
    const wireProvider: Provider = targetProvider === 'stub' ? 'codex' : targetProvider
    const turn: Turn = {
      id: crypto.randomUUID(),
      userMessageId: prep.userMessageId,
      prompt: prep.prompt,
      ...(prep.attachments?.length ? { attachments: prep.attachments } : {}),
      time: source.time,
      authorId: workspace.user.id,
      provider: targetProvider,
      ...(resolvedDisplay.modelId ? { modelId: resolvedDisplay.modelId } : {}),
      model: resolvedDisplay.modelLabel,
      seq: 0,
      state: 'running',
      parts: [],
    }
    updateChat(chatId, value => ({
      ...value,
      provider: targetProvider,
      serverProvider: targetProvider,
      model: resolved.model,
      turns: [...value.turns.slice(0, index), turn],
    }))
    await executeTurnStream(chatId, turn.id, {
      reuseUserMessageId: prep.userMessageId,
      provider: targetProvider,
      model: { id: resolved.model, provider: wireProvider },
      ...(resolved.reasoning ? { reasoning: resolved.reasoning } : {}),
      target: { mode: 'chat' },
      prompt: prep.prompt,
    })
  } catch (error) {
    if (useChats.getState().remote === remote) {
      useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось перегенерировать ответ.' })
    }
  }
}

export async function editAndResendPrompt(chatId: string, turnId: string, newContent: string) {
  const trimmed = newContent.trim()
  if (!trimmed) return
  const { workspace, chats, remote, connected, serverModelsByProvider } = useChats.getState()
  const c = chats.find(item => item.id === chatId)
  if (!workspace || !c || runs.has(chatId) || (c.access !== 'owner' && c.access !== 'write')) return
  const index = c.turns.findIndex(t => t.id === turnId)
  const source = index >= 0 ? c.turns[index] : undefined
  if (!source || source.state === 'running') return

  if (!remote) {
    const model = models.find(m => m.id === c.model) ?? models[0]
    if (blockReason(useAccountStore.getState().quotas[model.provider])) return
    const parentId = previousTurn(c.turns, index)
    const history = historyUntil(c.turns, parentId)
    const turn: Turn = {
      parentId,
      target: source.target ?? c.target,
      id: crypto.randomUUID(),
      time: Date.now(),
      authorId: workspace.user.id,
      ...(source.attachments?.length ? { attachments: source.attachments } : {}),
      prompt: trimmed,
      model: model.label,
      modelId: model.id,
      provider: model.provider,
      seq: 0,
      state: 'running',
      parts: [],
    }
    updateChat(chatId, value => ({ ...value, turns: [...value.turns.slice(0, index), turn] }))
    await executeTurnStream(chatId, turn.id, {
      model: { id: model.id, provider: model.provider },
      target: turn.target!,
      prompt: trimmed,
      history,
    })
    return
  }

  if (!connected || !c.historyLoaded) return
  const targetMessageID = source.userMessageId ?? source.id
  const chatProvider: ServerProvider = c.provider ?? remote.provider
  const catalog = serverModelsByProvider[chatProvider] ?? []
  const serverModel = catalog.find(m => m.id === c.model)
  const wireProvider: Provider = chatProvider === 'stub' ? 'codex' : chatProvider

  try {
    const prep = await remote.editMessage(chatId, targetMessageID, trimmed)
    const turn: Turn = {
      id: crypto.randomUUID(),
      userMessageId: prep.userMessageId,
      prompt: trimmed,
      ...(prep.attachments?.length ? { attachments: prep.attachments } : {}),
      time: Date.now(),
      authorId: workspace.user.id,
      provider: chatProvider,
      modelId: c.model,
      model: serverModel?.name ?? (chatProvider === 'stub' ? 'StubProvider' : providerLabels[wireProvider]),
      seq: 0,
      state: 'running',
      parts: [],
    }
    updateChat(chatId, value => ({ ...value, turns: [...value.turns.slice(0, index), turn] }))
    await executeTurnStream(chatId, turn.id, {
      reuseUserMessageId: prep.userMessageId,
      provider: chatProvider,
      model: { id: c.model, provider: wireProvider },
      ...(c.reasoning ? { reasoning: c.reasoning } : {}),
      target: { mode: 'chat' },
      prompt: trimmed,
    })
  } catch (error) {
    if (useChats.getState().remote === remote) {
      useChats.setState({ connectionError: error instanceof Error ? error.message : 'Не удалось изменить сообщение.' })
    }
  }
}

export function sendPrompt(id: string, input: string) {
  return startRun(id, input)
}
export function retryAnswer(chatId: string, turnId: string, modelId: string) {
  return startRun(chatId, '', { turnId, modelId })
}
async function startRun(id: string, input: string, retry?: { turnId: string; modelId: string }) {
  const { workspace, chats, remote, connected, serverModelsByProvider, serverProviders, providersLoaded } = useChats.getState()
  const c = chats.find(item => item.id === id)
  if (remote && (!connected || !c?.historyLoaded || retry)) return
  if (!workspace || !c || runs.has(id) || (c.access !== 'owner' && c.access !== 'write')) return
  const index = retry ? c.turns.findIndex(t => t.id === retry.turnId) : -1
  const source = c.turns[index]
  const chatProvider: ServerProvider = remote ? (c.provider ?? remote.provider) : 'claude'
  if (remote && providersLoaded && !serverProviders.some(p => p.id === chatProvider && p.available)) return
  const catalog = remote ? (serverModelsByProvider[chatProvider] ?? []) : []
  const serverModel = catalog.find(m => m.id === c.model)
  if (remote && c.model !== 'default' && !serverModel) return
  const remoteProvider: Provider = chatProvider === 'stub' ? 'codex' : chatProvider
  const resolvedRemote = remote ? resolveModelDisplay(chatProvider, c.model, serverModelsByProvider) : undefined
  const model = remote
    ? { id: resolvedRemote?.modelId ?? c.model, provider: remoteProvider, label: resolvedRemote?.modelLabel ?? (chatProvider === 'stub' ? 'StubProvider' : providerLabels[remoteProvider]) }
    : models.find(m => m.id === (retry?.modelId ?? c.model))
  if (!model || (retry && (!source || source.state === 'running' || source.model === model.label))) return

  const draftAttachments = source ? [] : (c.attachments ?? [])
  if (draftAttachments.some(a => a.status === 'uploading')) return
  const readyAttachments = draftAttachments.filter(a => a.status === 'ready' && a.remote?.id)
  const prompt = source?.prompt ?? input.trim()
  if ((!prompt && readyAttachments.length === 0) || (!remote && blockReason(useAccountStore.getState().quotas[model.provider]))) return

  const attachmentIds = readyAttachments.map(a => a.remote!.id!)
  const messageAttachments = readyAttachments.map(a => a.remote!)
  if (!source) {
    for (const item of draftAttachments) {
      if (item.previewUrl && typeof URL !== 'undefined') URL.revokeObjectURL(item.previewUrl)
    }
  }

  const parentId = source ? previousTurn(c.turns, index) : c.turns.at(-1)?.id ?? null
  const history = remote ? undefined : historyUntil(c.turns, parentId)
  const target = source?.target ?? (source ? { mode: 'chat' } as const : c.target)
  const retryFields = source ? { retryOf: source.id } : {}
  const turn: Turn = {
    ...retryFields, parentId, target, id: crypto.randomUUID(), time: Date.now(), authorId: workspace.user.id,
    ...(messageAttachments.length ? { attachments: messageAttachments } : {}),
    prompt, model: remote && chatProvider === 'stub' ? 'StubProvider' : model.label, modelId: model.id, provider: chatProvider, seq: 0, state: 'running', parts: [],
  }
  updateChat(id, value => ({
    ...value,
    title: !remote && value.title === 'Новый чат' ? (prompt || messageAttachments[0]?.name || 'Новый чат') : value.title,
    draft: source ? value.draft : '',
    attachments: source ? value.attachments : [],
    attachmentError: undefined,
    model: model.id,
    target,
    turns: [...value.turns, turn],
  }))
  await executeTurnStream(
    id,
    turn.id,
    {
      ...(remote ? { provider: chatProvider } : {}),
      model: { id: model.id, provider: model.provider },
      ...(c.reasoning ? { reasoning: c.reasoning } : {}),
      target,
      prompt,
      ...(attachmentIds.length ? { attachmentIds } : {}),
      ...(history ? { history } : {}),
      ...retryFields,
    },
    remote ? prompt : undefined,
  )
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
