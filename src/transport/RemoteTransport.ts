import type {
  AccountingAdapter, AdminAdapter, AdminUser, IndexedCodexChat, LoginRequest, RunEvent, RunRequest, ServerChat, ServerError, ServerHealth,
  ServerDevice, ServerLoginResponse, ServerMessage, ServerModel, ServerSession, TransportAdapter, Workspace, WorkspaceAdapter,
} from '../protocol/types'

type Change =
  | { type: 'connection'; connected: boolean }
  | { type: 'settled'; chatId: string }
  | { type: 'expired' }

export class RemoteError extends Error {
  constructor(readonly status: number, message: string, code?: string) {
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

function unavailable(): never { throw Error('Эта функция доступна в демо-режиме.') }
const access = (chat: ServerChat, ownerId: string) => ({ id: chat.id, ownerId, access: 'owner' as const, grants: [] })

function readEvent(data: unknown): RunEvent | undefined {
  if (typeof data !== 'string') return
  let e
  try { e = JSON.parse(data) } catch { return }
  if (!e || typeof e.chatId !== 'string' || typeof e.messageId !== 'string' || !Number.isSafeInteger(e.seq) || e.seq < 1) return
  switch (e.type) {
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
  private socket: WebSocket | undefined
  private connecting = false
  private logoutRequest?: Promise<void>
  private reconnectTimer?: ReturnType<typeof setTimeout>
  private reconnectDelay = 500
  private readonly changes = new Set<(e: Change) => void>()
  private readonly responses = new Map<string, ReadableStreamDefaultController<RunEvent>>()
  private readonly lifetime = new AbortController()
  private devices: ServerDevice[] = []
  private chatList: ServerChat[] = []
  private indexedChats: IndexedCodexChat[] = []
  private refreshQueue: Promise<unknown> = Promise.resolve()
  provider: ServerHealth['provider'] = 'stub'
  readonly address: string

  constructor(address: string) {
    this.address = normalizeServerAddress(address)
  }

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
    login: async (request: LoginRequest) => {
      const session = await this.request<ServerLoginResponse>('/api/auth/login', {
        username: request.login, password: request.password, deviceName: request.deviceName,
      })
      return this.openSession(session, session.token)
    },
    logout: () => this.logoutRequest ??= this.logout(),
    createChat: (sourceId) => this.updateWorkspace(async () => {
      if (sourceId) unavailable()
      const chat = await this.request<ServerChat>('/api/chats', {})
      this.chatList = [chat, ...this.chatList]
      return { id: chat.id, workspace: this.snapshot() }
    }),
    revokeDevice: id => this.updateWorkspace(async () => {
      const isCurrent = this.snapshot().deviceId === id
      await this.request('/api/devices/' + encodeURIComponent(id), undefined, undefined, 'DELETE')
      if (isCurrent) {
        this.close()
        return null
      }
      return this.loadDevices()
    }),
    async setShare() { return unavailable() },
  }

  readonly admin: AdminAdapter = {
    list: signal => this.request<AdminUser[]>('/api/admin/users', undefined, signal),
    create: user => this.request<AdminUser>('/api/admin/users', user),
    update: async (id, changes) => { await this.request('/api/admin/users/' + encodeURIComponent(id), changes, undefined, 'PATCH') },
    revokeDevices: async id => { await this.request('/api/admin/users/' + encodeURIComponent(id) + '/devices', undefined, undefined, 'DELETE') },
  }

  subscribeAccount() { return () => {} }
  respondToPermission() { return false }

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
        : response.status === 401 ? 'Сессия закончилась. Войдите снова.'
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

  restore(token: string) {
    this.token = token
    return this.request<ServerSession>('/api/auth/me').then(session => this.openSession(session, token))
  }

  private async openSession(session: ServerSession, token: string) {
    this.token = token
    const [health, chats, devices] = await Promise.all([
      this.request<ServerHealth>('/api/health'),
      this.request<ServerChat[]>('/api/chats'),
      this.request<ServerDevice[]>('/api/devices'),
    ])
    this.provider = health.provider
    this.chatList = chats
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
      this.chatList = await this.request<ServerChat[]>('/api/chats')
      return this.snapshot()
    })
  }

  refreshDevices() {
    return this.updateWorkspace(() => this.loadDevices())
  }

  private async loadDevices() {
    this.devices = await this.request<ServerDevice[]>('/api/devices')
    return this.snapshot()
  }

  models() {
    return this.request<ServerModel[]>('/api/models')
  }

  async refreshCodexIndex() {
    try {
      this.indexedChats = await this.request<IndexedCodexChat[]>('/api/codex/chats')
    } catch (error) {
      if (error instanceof RemoteError && error.status === 404) return undefined
      throw error
    }
    return this.snapshot()
  }

  messages(chatId: string, signal: AbortSignal) {
    const collection = chatId.startsWith('codex-') ? '/api/codex/chats/' : '/api/chats/'
    return this.request<ServerMessage[]>(collection + encodeURIComponent(chatId) + '/messages', undefined, signal)
  }

  private snapshot(): Workspace {
    if (!this.session) throw Error('Сессия закончилась. Войдите снова.')
    const { user, device } = this.session
    const deviceId = this.devices.find(entry => entry.isCurrent)?.id ?? device.id
    const member = { id: user.id, name: user.username, role: user.role ?? 'user', isActive: user.isActive ?? true }
    return {
      user: member, deviceId, users: [member],
      devices: this.devices.map(entry => ({
        id: entry.id, ownerId: entry.userId, name: entry.name, online: entry.id === deviceId,
      })),
      chats: [...this.chatList.map(chat => access(chat, user.id)),
        ...this.indexedChats.map(chat => ({ ...access(chat, user.id), access: 'read' as const }))],
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
    if (!this.session || this.socket || this.connecting || this.logoutRequest || this.lifetime.signal.aborted) return
    clearTimeout(this.reconnectTimer)
    this.connecting = true
    let ticket: string
    try {
      ({ ticket } = await this.request<{ ticket: string }>('/api/ws/ticket', undefined, undefined, 'POST'))
      if (typeof ticket !== 'string' || !ticket) throw Error('Сервер не выдал тикет WebSocket.')
    } catch (error) {
      if (!(error instanceof RemoteError && error.sessionExpired)) this.scheduleReconnect()
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
      this.reconnectDelay = 500
      this.emit({ type: 'connection', connected: true })
    }
    socket.onmessage = ({ data }) => {
      if (socket !== this.socket) return
      const event = readEvent(data)
      if (!event) return
      const key = event.chatId + '/' + event.messageId
      const pending = this.responses.get(key)
      pending?.enqueue(event)
      if (event.type === 'done' || event.type === 'error') {
        if (pending) {
          this.responses.delete(key)
          pending.close()
        }
        if (event.type === 'done' || pending) this.emit({ type: 'settled', chatId: event.chatId })
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
    this.reconnectTimer = setTimeout(() => void this.connect(), this.reconnectDelay)
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, 5000)
  }

  private async logout() {
    if (this.token) {
      try {
        await fetch(this.address + '/api/auth/logout', {
          method: 'POST', headers: { Authorization: 'Bearer ' + this.token },
          keepalive: true, signal: AbortSignal.timeout(1000), redirect: 'error',
        })
      } catch { /* The local session still ends if the server is unreachable. */ }
    }
    this.close()
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
          const { chatId, messageId, model, target, prompt } = request
          socket.send(JSON.stringify({ chatId, messageId, model, target, prompt }))
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
