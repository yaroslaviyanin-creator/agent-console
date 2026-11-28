import type {
  AccountingAdapter, AdminChat, AdminUser, ChatAccess, ChatFolder, ServerAdminAdapter, IndexedCodexChat, LoginRequest, MessageAttachment, RunEvent, RunRequest, SearchResult, ServerChat, ServerCreateChatRequest, ServerError, ServerHealth,
  ServerDevice, ServerDirectoryUser, ServerLoginResponse, ServerMessage, ServerModel, ServerProvider, ServerProviderInfo, ServerProvidersResponse, ServerQuota, ServerSession, TransportAdapter, UserProfile, UserProfilePatch, Workspace, WorkspaceAdapter,
} from '../protocol/types'

type Change =
  | { type: 'connection'; connected: boolean }
  | { type: 'settled'; chatId: string }
  | { type: 'run'; event: RunEvent }
  | { type: 'workspace_changed' }
  | { type: 'expired' }
  | { type: 'pending' }

export class RemoteError extends Error {
  constructor(readonly status: number, message: string, readonly code?: string) {
    super(message)
    this.sessionExpired = status === 401 || code === 'account_inactive'
  }
  readonly sessionExpired: boolean
}

export function normalizeServerAddress(address: string) {
  let url: URL
  try { url = new URL(address.trim()) } catch { throw Error('Укажите адрес сервера, например http://127.0.0.1:8080.') }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw Error('Укажите HTTP(S)-адрес сервера без пути, логина или пароля.')
  }
  return url.origin
}

const KNOWN_SERVER_PROVIDERS: readonly ServerProvider[] = ['antigravity', 'codex', 'claude', 'paid_api']

export function fallbackProviders(defaultProvider: ServerProvider): ServerProvidersResponse {
  const providers: ServerProviderInfo[] = KNOWN_SERVER_PROVIDERS.map(id => ({
    id,
    available: id === defaultProvider,
  }))
  if (defaultProvider === 'stub') {
    providers.push({ id: 'stub', available: true })
  }
  return { defaultProvider, providers }
}

function unavailable(): never { throw Error('Эта функция доступна в демо-режиме.') }
const chatAccess = (chat: ServerChat, userId: string): ChatAccess => ({
  id: chat.id,
  ownerId: chat.ownerId ?? userId,
  access: chat.access ?? 'owner',
  grants: chat.access === 'owner' ? chat.grants ?? [] : [],
  ...(chat.pinned !== undefined ? { pinned: Boolean(chat.pinned) } : {}),
  ...(chat.folderId !== undefined ? { folderId: chat.folderId } : {}),
  ...(chat.archivedAt !== undefined ? { archivedAt: chat.archivedAt } : {}),
})

function readEvent(data: unknown): RunEvent | { type: 'workspace_changed' } | undefined {
  if (typeof data !== 'string') return
  let e
  try { e = JSON.parse(data) } catch { return }
  if (e?.type === 'workspace_changed') return { type: 'workspace_changed' }
  if (!e || typeof e.chatId !== 'string' || typeof e.messageId !== 'string' || !Number.isSafeInteger(e.seq) || e.seq < 1) return
  switch (e.type) {
    case 'started':
      return typeof e.prompt === 'string' && typeof e.model === 'string' && Number.isSafeInteger(e.createdAt)
        ? { ...e, ...(Array.isArray(e.attachments) ? { attachments: e.attachments } : {}) }
        : undefined
    case 'web_sources':
      return Array.isArray(e.webSources) ? e : undefined
    case 'text_delta':
    case 'thinking_delta':
      return typeof e.delta === 'string' ? e : undefined
    case 'done':
      return ['completed', 'refused', 'denied'].includes(e.reason) ? e : undefined
    case 'error':
      return typeof e.message === 'string' && typeof e.code === 'string' && typeof e.retryable === 'boolean' ? e : undefined
  }
}

export class RemoteTransport implements TransportAdapter {
  private token = ''
  private session: ServerSession | undefined
  private initialQuota: ServerQuota | null = null
  private socket: WebSocket | undefined
  private connecting = false
  private logoutRequest?: Promise<void>
  private reconnectTimer?: ReturnType<typeof setTimeout>
  private reconnectDelay = 1000
  private readonly changes = new Set<(e: Change) => void>()
  private readonly responses = new Map<string, ReadableStreamDefaultController<RunEvent>>()
  private readonly lifetime = new AbortController()
  private devices: ServerDevice[] = []
  private users: ServerDirectoryUser[] = []
  private chatList: ServerChat[] = []
  private indexedChats: IndexedCodexChat[] = []
  private refreshQueue: Promise<unknown> = Promise.resolve()
  provider: ServerHealth['provider'] = 'stub'
  readonly address: string

  constructor(address: string) {
    this.address = normalizeServerAddress(address)
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('online', () => this.reconnectNow(), { signal: this.lifetime.signal })
    }
    if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
      document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') this.reconnectNow()
      }, { signal: this.lifetime.signal })
    }
  }

  reconnectNow() {
    if (this.socket || this.connecting || this.logoutRequest || this.lifetime.signal.aborted) return
    this.reconnectDelay = 1000
    clearTimeout(this.reconnectTimer)
    void this.connect()
  }

  get sessionQuota() { return this.initialQuota }
  get sessionToken() { return this.token }
  get connected() { return this.socket?.readyState === WebSocket.OPEN }
  get chats() { return [...this.chatList, ...this.indexedChats] }

  readonly accounting: AccountingAdapter = {
    async requestTransfer() { unavailable() },
    async resolveTransfer() { unavailable() },
    async billing() { unavailable() },
    async setHardLimit() { unavailable() },
    async closePeriod() { unavailable() },
  }

  readonly workspace: WorkspaceAdapter = {
    login: request => this.authenticate(request, 'login'),
    logout: () => this.logoutRequest ??= this.logout(),
    createChat: (sourceId) => sourceId
      ? this.updateWorkspace(async () => {
          const chat = await this.request<ServerChat>('/api/chats/' + encodeURIComponent(sourceId) + '/fork', undefined, undefined, 'POST')
          this.chatList = [chat, ...this.chatList]
          return { id: chat.id, workspace: this.snapshot() }
        })
      : this.createChat(),
    revokeDevice: id => this.updateWorkspace(async () => {
      const isCurrent = this.snapshot().deviceId === id
      await this.request('/api/devices/' + encodeURIComponent(id), undefined, undefined, 'DELETE')
      if (isCurrent) {
        this.close()
        return null
      }
      return this.loadDevices()
    }),
    setShare: (chatId, userId, access, _trust) => this.updateWorkspace(async () => {
      const path = '/api/chats/' + encodeURIComponent(chatId) + '/shares/' + encodeURIComponent(userId)
      await this.request(path, access ? { access } : undefined, undefined, access ? 'PUT' : 'DELETE')
      await this.loadChatsAndUsers()
      return this.snapshot()
    }),
  }

  readonly admin: ServerAdminAdapter = {
    list: signal => this.request<AdminUser[]>('/api/admin/users', undefined, signal),
    create: user => this.request<AdminUser>('/api/admin/users', user),
    update: async (id, changes) => { await this.request('/api/admin/users/' + encodeURIComponent(id), changes, undefined, 'PATCH') },
    chats: (id, signal) => this.request<AdminChat[]>('/api/admin/users/' + encodeURIComponent(id) + '/chats', undefined, signal),
    messages: (id, chatId, signal) => this.request<ServerMessage[]>('/api/admin/users/' + encodeURIComponent(id) + '/chats/' + encodeURIComponent(chatId) + '/messages', undefined, signal),
    revokeDevices: async id => { await this.request('/api/admin/users/' + encodeURIComponent(id) + '/devices', undefined, undefined, 'DELETE') },
    delete: async id => { await this.request('/api/admin/users/' + encodeURIComponent(id), undefined, undefined, 'DELETE') },
  }

  getProfile() {
    return this.request<UserProfile>('/api/profile')
  }

  async updateProfile(patch: UserProfilePatch) {
    const profile = await this.request<UserProfile>('/api/profile', patch, undefined, 'PATCH')
    if (this.session) {
      this.session = {
        ...this.session,
        user: {
          ...this.session.user,
          displayName: profile.displayName,
          avatarData: profile.avatarData,
        },
      }
    }
    return profile
  }

  subscribeAccount() { return () => {} }
  respondToPermission() { return false }

  async uploadAttachments(
    chatId: string,
    files: File[],
    signalOrOptions?: AbortSignal | { signal?: AbortSignal; onProgress?: (percent: number) => void },
  ): Promise<MessageAttachment[]> {
    const opts = signalOrOptions instanceof AbortSignal ? { signal: signalOrOptions } : (signalOrOptions ?? {})
    const form = new FormData()
    for (const file of files) {
      form.append('files', file, file.name)
    }
    const url = this.address + '/api/chats/' + encodeURIComponent(chatId) + '/attachments'
    const mapFailure = (status: number, detail: Partial<ServerError>) => {
      const message = status === 429 || detail.code === 'rate_limited'
        ? 'Слишком много загрузок. Попробуйте позже.'
        : status === 413
          ? (detail.message ?? 'Размер загрузки превышает допустимый лимит (100 МБ).')
          : status === 401
            ? 'Сессия закончилась. Войдите снова.'
            : detail.message ?? 'Не удалось загрузить файл.'
      const error = new RemoteError(status, message, detail.code)
      if (error.sessionExpired && this.session) this.emit({ type: 'expired' })
      return error
    }

    if (typeof XMLHttpRequest !== 'undefined') {
      return await new Promise<MessageAttachment[]>((resolve, reject) => {
        const xhr = new XMLHttpRequest()
        const combinedSignal = AbortSignal.any([this.lifetime.signal, ...(opts.signal ? [opts.signal] : [])])
        const onAbort = () => {
          xhr.abort()
          reject(Error('Загрузка отменена.'))
        }
        if (combinedSignal.aborted) {
          reject(Error('Загрузка отменена.'))
          return
        }
        combinedSignal.addEventListener('abort', onAbort, { once: true })
        const cleanup = () => combinedSignal.removeEventListener('abort', onAbort)

        xhr.open('POST', url)
        xhr.timeout = 600_000
        if (this.token) xhr.setRequestHeader('Authorization', 'Bearer ' + this.token)
        xhr.upload.onprogress = evt => {
          if (evt.lengthComputable && evt.total > 0 && opts.onProgress) {
            opts.onProgress(Math.min(100, Math.round((evt.loaded / evt.total) * 100)))
          }
        }
        xhr.onerror = () => {
          cleanup()
          reject(Error('Нет связи с сервером при загрузке файла.'))
        }
        xhr.ontimeout = () => {
          cleanup()
          reject(Error('Превышено время ожидания загрузки файла.'))
        }
        xhr.onload = () => {
          cleanup()
          let parsed: unknown = {}
          try {
            parsed = xhr.responseText ? JSON.parse(xhr.responseText) : {}
          } catch {
            parsed = {}
          }
          if (xhr.status < 200 || xhr.status >= 300) {
            reject(mapFailure(xhr.status, (parsed ?? {}) as Partial<ServerError>))
            return
          }
          const body = (parsed ?? {}) as { attachments?: MessageAttachment[] }
          resolve(body.attachments ?? [])
        }
        xhr.send(form)
      })
    }

    const headers = new Headers()
    if (this.token) headers.set('Authorization', 'Bearer ' + this.token)
    let response: Response
    try {
      response = await fetch(url, {
        method: 'POST',
        headers,
        body: form,
        signal: AbortSignal.any([this.lifetime.signal, AbortSignal.timeout(600_000), ...(opts.signal ? [opts.signal] : [])]),
        cache: 'no-store',
        redirect: 'error',
      })
    } catch {
      throw Error('Нет связи с сервером при загрузке файла.')
    }
    if (!response.ok) {
      const detail: Partial<ServerError> = await response.json().catch(() => ({})) ?? {}
      throw mapFailure(response.status, detail)
    }
    const body = await response.json() as { attachments?: MessageAttachment[] }
    return body.attachments ?? []
  }

  deleteAttachment(attachmentId: string) {
    return this.request<void>('/api/attachments/' + encodeURIComponent(attachmentId), undefined, undefined, 'DELETE')
  }

  async fetchAttachmentBlob(urlPath: string, signal?: AbortSignal): Promise<Blob> {
    const headers = new Headers()
    if (this.token) headers.set('Authorization', 'Bearer ' + this.token)
    const response = await fetch(this.address + urlPath, {
      headers,
      signal: AbortSignal.any([this.lifetime.signal, AbortSignal.timeout(20_000), ...(signal ? [signal] : [])]),
      cache: 'no-store',
      redirect: 'error',
    })
    if (!response.ok) {
      throw new RemoteError(response.status, 'Не удалось загрузить вложение.')
    }
    return response.blob()
  }

  async fetchAttachmentBlobUrl(urlPath: string, signal?: AbortSignal): Promise<string> {
    const blob = await this.fetchAttachmentBlob(urlPath, signal)
    return URL.createObjectURL(blob)
  }

  async downloadAttachment(att: MessageAttachment): Promise<void> {
    if (!att.url) return
    const sep = att.url.includes('?') ? '&' : '?'
    const blobUrl = await this.fetchAttachmentBlobUrl(att.url + sep + 'download=1')
    try {
      const a = document.createElement('a')
      a.href = blobUrl
      a.download = att.name || 'attachment'
      document.body.appendChild(a)
      a.click()
      a.remove()
    } finally {
      setTimeout(() => URL.revokeObjectURL(blobUrl), 1000)
    }
  }

  private async request<T>(path: string, body?: object, signal?: AbortSignal, method = body ? 'POST' : 'GET') {
    const headers = new Headers()
    if (this.token) headers.set('Authorization', 'Bearer ' + this.token)
    if (body) headers.set('Content-Type', 'application/json')
    let response: Response
    try {
      response = await fetch(this.address + path, {
        method, headers, body: body ? JSON.stringify(body) : null,
        signal: AbortSignal.any([this.lifetime.signal, AbortSignal.timeout(10_000), ...(signal ? [signal] : [])]),
        cache: 'no-store', redirect: 'error',
      })
    } catch {
      throw Error('Нет связи с сервером. Проверьте адрес и доступность сервера.')
    }
    if (!response.ok) {
      const detail: Partial<ServerError> = await response.json().catch(() => ({})) ?? {}
      const message = path === '/api/auth/login' && response.status === 401
        ? 'Неверный логин или пароль.'
        : response.status === 429 || detail.code === 'rate_limited'
          ? (path.startsWith('/api/auth/') ? 'Слишком много попыток входа. Попробуйте позже.' : 'Слишком много запросов. Попробуйте позже.')
        : response.status === 401 ? 'Сессия закончилась. Войдите снова.'
        : detail.code === 'account_pending' ? 'Аккаунт ожидает выдачи доступа.'
        : detail.code === 'invalid_registration' ? 'Проверьте логин, пароль и название устройства.'
        : detail.code === 'account_inactive' ? 'Аккаунт заблокирован.'
        : detail.code === 'admin_required' ? 'Требуются права администратора.'
        : detail.code === 'username_taken' ? 'Этот логин уже занят.'
        : detail.message ?? 'Сервер отклонил запрос.'
      const error = new RemoteError(response.status, message, detail.code)
      if (error.sessionExpired && this.session) this.emit({ type: 'expired' })
      throw error
    }
    return (response.status === 204 ? undefined : await response.json()) as T
  }

  async register(request: LoginRequest): Promise<Workspace | { pending: true; user: ServerLoginResponse['user'] }> {
    const res = await this.request<ServerLoginResponse | { user: ServerLoginResponse['user']; pending: true }>('/api/auth/register', {
      username: request.login, password: request.password, deviceName: request.deviceName,
    })
    if ('pending' in res && res.pending) {
      return { pending: true, user: res.user }
    }
    const session = res as ServerLoginResponse
    return this.openSession(session, session.token)
  }

  private async authenticate(request: LoginRequest, action: 'login' | 'register') {
    const session = await this.request<ServerLoginResponse>('/api/auth/' + action, {
      username: request.login, password: request.password, deviceName: request.deviceName,
    })
    return this.openSession(session, session.token)
  }

  restore(token: string) {
    this.token = token
    return this.request<ServerSession>('/api/auth/me').then(session => this.openSession(session, token))
  }

  async checkStatus() {
    const session = await this.request<ServerSession>('/api/auth/me')
    return this.openSession(session, this.token)
  }

  quota(signal?: AbortSignal) {
    return this.request<ServerQuota>('/api/quotas/current', undefined, signal)
  }

  private async openSession(session: ServerSession, token: string) {
    this.token = token
    if (session.user.role === 'pending') {
      this.initialQuota = null
      this.session = session
      this.chatList = []
      this.indexedChats = []
      this.devices = [session.device]
      this.users = []
      return this.snapshot()
    }
    const [health, devices, quota] = await Promise.all([
      this.request<ServerHealth>('/api/health'),
      this.request<ServerDevice[]>('/api/devices'),
      this.quota().catch(() => null),
      this.loadChatsAndUsers(),
    ])
    this.initialQuota = quota
    this.provider = health.provider
    this.devices = devices
    this.session = session
    return this.snapshot()
  }

  private updateWorkspace<T>(operation: () => Promise<T>) {
    const next = this.refreshQueue.then(operation)
    this.refreshQueue = next.catch(() => {})
    return next
  }

  refreshChats() {
    return this.updateWorkspace(async () => {
      await this.loadChatsAndUsers()
      return this.snapshot()
    })
  }

  private async loadChatsAndUsers() {
    this.chatList = await this.request<ServerChat[]>('/api/chats')
  }

  searchUsers(q: string, limit = 10, signal?: AbortSignal) {
    const trimmed = q.trim()
    if (trimmed.length < 2) return Promise.resolve([] as ServerDirectoryUser[])
    return this.request<ServerDirectoryUser[]>(
      `/api/users?q=${encodeURIComponent(trimmed)}&limit=${Math.min(20, Math.max(1, limit))}`,
      undefined,
      signal,
    )
  }

  logoutAllDevices(includeCurrent = false) {
    return this.updateWorkspace(async () => {
      await this.request('/api/auth/logout-all', { includeCurrent }, undefined, 'POST')
      if (includeCurrent) {
        this.close()
        return null
      }
      return this.loadDevices()
    })
  }

  refreshDevices() {
    return this.updateWorkspace(() => this.loadDevices())
  }

  private async loadDevices() {
    this.devices = await this.request<ServerDevice[]>('/api/devices')
    return this.snapshot()
  }

  createChat(options: ServerCreateChatRequest = {}) {
    return this.updateWorkspace(async () => {
      const body: ServerCreateChatRequest = {}
      if (options.provider) body.provider = options.provider
      if (options.model) body.model = options.model
      if (options.title) body.title = options.title
      const chat = await this.request<ServerChat>('/api/chats', body, undefined, 'POST')
      const pinned = this.chatList.filter(c => c.pinned)
      const regular = this.chatList.filter(c => !c.pinned)
      this.chatList = [...pinned, chat, ...regular]
      return { id: chat.id, workspace: this.snapshot() }
    })
  }

  forkChat(chatId: string, upToMessageId?: string) {
    return this.updateWorkspace(async () => {
      const chat = await this.request<ServerChat>(
        '/api/chats/' + encodeURIComponent(chatId) + '/fork',
        upToMessageId ? { upToMessageId } : undefined,
        undefined,
        'POST',
      )
      await this.loadChatsAndUsers()
      return { id: chat.id, workspace: this.snapshot() }
    })
  }

  patchChat(chatId: string, patch: { title?: string; pinned?: boolean; folderId?: string | null; archived?: boolean }) {
    return this.updateWorkspace(async () => {
      const updated = await this.request<ServerChat>('/api/chats/' + encodeURIComponent(chatId), patch, undefined, 'PATCH')
      await this.loadChatsAndUsers()
      return { chat: updated, workspace: this.snapshot() }
    })
  }

  folders(signal?: AbortSignal) {
    return this.request<ChatFolder[]>('/api/folders', undefined, signal)
  }

  createFolder(name: string, color?: string) {
    return this.request<ChatFolder>('/api/folders', { name, ...(color ? { color } : {}) }, undefined, 'POST')
  }

  updateFolder(id: string, patch: { name?: string; color?: string; sortOrder?: number }) {
    return this.request<ChatFolder>('/api/folders/' + encodeURIComponent(id), patch, undefined, 'PATCH')
  }

  deleteFolder(id: string) {
    return this.request<void>('/api/folders/' + encodeURIComponent(id), undefined, undefined, 'DELETE')
  }

  search(query: string, limit = 30, signal?: AbortSignal) {
    const q = query.trim()
    if (!q) return Promise.resolve([] as SearchResult[])
    return this.request<SearchResult[]>(
      `/api/search?q=${encodeURIComponent(q)}&limit=${Math.min(30, Math.max(1, limit))}`,
      undefined,
      signal,
    )
  }

  deleteChat(chatId: string) {
    return this.updateWorkspace(async () => {
      await this.request('/api/chats/' + encodeURIComponent(chatId), undefined, undefined, 'DELETE')
      await this.loadChatsAndUsers()
      return this.snapshot()
    })
  }

  deleteMessage(chatId: string, messageId: string) {
    return this.request<void>(
      '/api/chats/' + encodeURIComponent(chatId) + '/messages/' + encodeURIComponent(messageId),
      undefined,
      undefined,
      'DELETE',
    )
  }

  regenerateMessage(chatId: string, messageId: string, options?: { provider?: ServerProvider; model?: string }) {
    const body: { provider?: ServerProvider; model?: string } = {}
    if (options?.provider) body.provider = options.provider
    if (options?.model) body.model = options.model
    return this.request<{ userMessageId: string; prompt: string; attachments?: MessageAttachment[] }>(
      '/api/chats/' + encodeURIComponent(chatId) + '/messages/' + encodeURIComponent(messageId) + '/regenerate',
      body,
      undefined,
      'POST',
    )
  }

  editMessage(chatId: string, messageId: string, content: string) {
    return this.request<{ userMessageId: string; prompt: string; attachments?: MessageAttachment[] }>(
      '/api/chats/' + encodeURIComponent(chatId) + '/messages/' + encodeURIComponent(messageId),
      { content },
      undefined,
      'PATCH',
    )
  }

  async providers(signal?: AbortSignal): Promise<ServerProvidersResponse> {
    try {
      return await this.request<ServerProvidersResponse>('/api/providers', undefined, signal)
    } catch (error) {
      if (error instanceof RemoteError && error.status === 404) {
        return fallbackProviders(this.provider)
      }
      throw error
    }
  }

  models(provider?: ServerProvider, signal?: AbortSignal) {
    const query = provider ? '?provider=' + encodeURIComponent(provider) : ''
    return this.request<ServerModel[]>('/api/models' + query, undefined, signal)
  }

  messages(chatId: string, signal: AbortSignal) {
    return this.request<ServerMessage[]>('/api/chats/' + encodeURIComponent(chatId) + '/messages', undefined, signal)
  }

  private snapshot(): Workspace {
    if (!this.session) throw Error('Сессия закончилась. Войдите снова.')
    const { user, device } = this.session
    const deviceId = this.devices.find(entry => entry.isCurrent)?.id ?? device.id
    const member = {
      id: user.id,
      name: user.username,
      ...(user.displayName ? { displayName: user.displayName } : {}),
      ...(user.avatarData ? { avatarData: user.avatarData } : {}),
      role: user.role ?? 'user',
      isActive: user.isActive ?? true,
    }
    return {
      user: member,
      deviceId,
      users: [
        member,
        ...this.users.filter(u => u.id !== user.id).map(u => ({
          id: u.id,
          name: u.username,
          ...(u.displayName ? { displayName: u.displayName } : {}),
          ...(u.avatarData ? { avatarData: u.avatarData } : {}),
        })),
      ],
      devices: this.devices.map(entry => ({
        id: entry.id, ownerId: entry.userId, name: entry.name, online: entry.id === deviceId, lastSeenAt: entry.lastSeenAt,
      })),
      chats: [...this.chatList.map(chat => chatAccess(chat, user.id)),
        ...this.indexedChats.map(chat => ({ id: chat.id, ownerId: user.id, access: 'read' as const, grants: [] }))],
    }
  }

  subscribe(listener: (event: Change) => void) {
    this.changes.add(listener)
    return () => { this.changes.delete(listener) }
  }

  private emit(event: Change) {
    for (const listener of this.changes) listener(event)
  }

  async connect() {
    if (!this.session || this.session.user.role === 'pending' || this.socket || this.connecting || this.logoutRequest || this.lifetime.signal.aborted) return
    clearTimeout(this.reconnectTimer)
    this.connecting = true
    let ticket: string
    try {
      ({ ticket } = await this.request<{ ticket: string }>('/api/ws/ticket', undefined, undefined, 'POST'))
      if (typeof ticket !== 'string' || !ticket) throw Error('Сервер не выдал тикет WebSocket.')
    } catch (error) {
      if (error instanceof RemoteError && error.code === 'account_pending') this.emit({ type: 'pending' })
      else if (!(error instanceof RemoteError && error.sessionExpired)) this.scheduleReconnect()
      return
    } finally {
      this.connecting = false
    }
    if (this.logoutRequest || this.lifetime.signal.aborted) return
    const url = new URL('/ws', this.address)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.searchParams.set('ticket', ticket)
    let socket: WebSocket
    try { socket = new WebSocket(url) }
    catch { this.scheduleReconnect(); return }
    this.socket = socket
    const opening = setTimeout(() => socket.close(), 10_000)
    socket.onopen = () => {
      clearTimeout(opening)
      if (this.socket !== socket) return
      this.reconnectDelay = 1000
      this.emit({ type: 'connection', connected: true })
    }
    socket.onmessage = ({ data }) => {
      if (socket !== this.socket) return
      const event = readEvent(data)
      if (!event) return
      if (event.type === 'workspace_changed') {
        this.emit(event)
        return
      }
      const key = event.chatId + '/' + event.messageId
      const pending = this.responses.get(key)
      if (pending) pending.enqueue(event)
      else this.emit({ type: 'run', event })
      if (event.type === 'done' || event.type === 'error') {
        if (pending) {
          this.responses.delete(key)
          pending.close()
        }
        if (event.type === 'done' || pending || event.seq > 1) this.emit({ type: 'settled', chatId: event.chatId })
      }
    }
    socket.onclose = () => {
      clearTimeout(opening)
      if (socket !== this.socket) return
      this.socket = undefined
      this.failStreams(Error('Связь прервалась. История обновится после переподключения.'))
      this.emit({ type: 'connection', connected: false })
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect() {
    if (this.logoutRequest || this.lifetime.signal.aborted) return
    clearTimeout(this.reconnectTimer)
    const jitter = 0.85 + Math.random() * 0.3
    const delay = Math.round(this.reconnectDelay * jitter)
    this.reconnectTimer = setTimeout(() => void this.connect(), delay)
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, 15_000)
  }

  private async logout() {
    const token = this.token
    this.close()
    if (token) {
      try {
        await fetch(this.address + '/api/auth/logout', {
          method: 'POST', headers: { Authorization: 'Bearer ' + token },
          keepalive: true, signal: AbortSignal.timeout(1000), redirect: 'error',
        })
      } catch { /* The local session is already closed. */ }
    }
  }

  close() {
    this.lifetime.abort()
    clearTimeout(this.reconnectTimer)
    const socket = this.socket
    this.socket = undefined
    socket?.close()
    this.token = ''
    this.session = undefined
    this.devices = []
    this.users = []
    this.chatList = []
    this.indexedChats = []
    this.failStreams(new DOMException('Сессия закрыта.', 'AbortError'))
    this.changes.clear()
  }

  private failStreams(error: Error) {
    for (const controller of this.responses.values()) controller.error(error)
    this.responses.clear()
  }

  async *stream(request: RunRequest, signal: AbortSignal): AsyncGenerator<RunEvent> {
    signal.throwIfAborted()
    if (request.target.mode !== 'chat' || request.retryOf) unavailable()
    const socket = this.socket
    if (!socket || !this.connected) throw Error('Идёт подключение к серверу. Попробуйте ещё раз.')
    const key = request.chatId + '/' + request.messageId
    const queue = new ReadableStream<RunEvent>({
      start: controller => {
        if (this.responses.has(key)) throw Error('Запрос уже отправлен.')
        this.responses.set(key, controller)
        try {
          // SQLite history is authoritative in server mode.
          const { chatId, messageId, reuseUserMessageId, provider, model, target, prompt, reasoning, attachmentIds } = request
          socket.send(JSON.stringify({
            chatId,
            messageId,
            ...(reuseUserMessageId ? { reuseUserMessageId } : {}),
            ...(provider ? { provider } : {}),
            model,
            target,
            prompt,
            ...(attachmentIds?.length ? { attachmentIds } : {}),
            ...(reasoning ? { reasoning } : {}),
          }))
        } catch (error) {
          this.responses.delete(key)
          throw error
        }
      },
    }, { highWaterMark: 0 })
    const abort = () => {
      this.responses.get(key)?.error(signal.reason)
      this.responses.delete(key)
    }
    const reader = queue.getReader()
    signal.addEventListener('abort', abort, { once: true })
    try {
      for (;;) {
        const item = await reader.read()
        signal.throwIfAborted()
        if (item.done) return
        yield item.value
      }
    } finally {
      signal.removeEventListener('abort', abort)
      this.responses.delete(key)
      await reader.cancel().catch(() => {})
      reader.releaseLock()
    }
  }
}
