import type { ChatAccess, Device, Grant, LoginRequest, ShareAccess, Target, Workspace, WorkspaceAdapter } from '../protocol/types'

export const users = [
  { id: 'demo', name: 'Демо-пользователь' }, { id: 'alex', name: 'Алексей' }, { id: 'maria', name: 'Мария' },
]
interface Room { id: string; ownerId: string; grants: Grant[]; pinned?: boolean }
function room(id: string, ownerId: string, grants: Grant[] = []): Room { return { id, ownerId, grants } }

export class MockWorkspace implements WorkspaceAdapter {
  private rooms: Room[] = [
    ...['timeout', 'review', 'tests'].map(id => room(id, 'demo')),
    room('shared', 'alex', [{ userId: 'demo', access: 'read' }]),
    room('branch', 'maria', [{ userId: 'demo', access: 'fork' }]),
    room('team', 'alex', [{ userId: 'demo', access: 'write' }]),
  ]
  private devices: Device[] = [
    { id: 'home', ownerId: 'demo', name: 'Домашний ПК', online: true },
    { id: 'archive', ownerId: 'demo', name: 'Запасной ноутбук', online: false },
  ]
  private bindings = new Map<string, string>()
  private session: { userId: string; deviceId: string } | null = null
  get userId() { return this.session?.userId }
  constructor(private readonly invalidate: (deviceId?: string) => void) {}

  async login(request: LoginRequest) {
    let host = ''
    try { host = atob(request.connection.trim()) } catch { /* invalid encoding */ }
    if (host !== 'mock://agent-console') throw Error('Нужна строка подключения демо-сервера. Её можно подставить кнопкой QR.')
    const user = users.find(u => u.id === request.login.trim().toLowerCase())
    if (!user || request.password !== 'demo') throw Error('Проверьте логин и демо-пароль.')
    const name = request.deviceName.trim()
    if (!name || name.length > 80) throw Error('Введите название устройства длиной до 80 символов.')
    this.logout()
    let device = this.devices.find(d => d.id === this.bindings.get(user.id))
    if (!device) {
      device = { id: crypto.randomUUID(), name, ownerId: user.id, online: true }
      this.devices.push(device)
      this.bindings.set(user.id, device.id)
    }
    Object.assign(device, { name, online: true })
    this.session = { userId: user.id, deviceId: device.id }
    return this.snapshot()
  }
  logout() {
    // Demo MCP endpoints remain online independently of this GUI login.
    this.session = null
    this.invalidate()
  }
  private requireSession() {
    if (!this.session) throw Error('Войдите в рабочее пространство.')
    return this.session
  }
  private snapshot(): Workspace {
    const { userId, deviceId } = this.requireSession()
    const chats: ChatAccess[] = []
    for (const r of this.rooms) {
      if (r.ownerId === userId) chats.push({ id: r.id, ownerId: userId, access: 'owner', grants: r.grants, ...(r.pinned ? { pinned: true } : {}) })
      else {
        const g = r.grants.find(g => g.userId === userId)
        if (g) chats.push({ ...g, id: r.id, ownerId: r.ownerId, grants: [] })
      }
    }
    return structuredClone({
      user: users.find(u => u.id === userId)!, deviceId, users, chats,
      devices: this.devices.filter(d => d.ownerId === userId || chats.some(c => c.trustedDeviceId === d.id)),
    })
  }
  async revokeDevice(id: string) {
    const session = this.requireSession()
    if (!this.devices.some(d => d.id === id && d.ownerId === session.userId)) throw Error('Нет доступа к устройству.')
    this.devices = this.devices.filter(d => d.id !== id)
    for (const r of this.rooms) for (const g of r.grants) if (g.trustedDeviceId === id) delete g.trustedDeviceId
    if (session.deviceId === id) { this.logout(); return null }
    this.invalidate(id)
    return this.snapshot()
  }
  async setShare(chatId: string, userId: string, access: ShareAccess | null, trust: boolean) {
    const session = this.requireSession()
    const r = this.rooms.find(room => room.id === chatId && room.ownerId === session.userId)
    if (!r || !users.some(u => u.id === userId) || userId === session.userId) throw Error('Нет доступа к изменению участников.')
    r.grants = r.grants.filter(g => g.userId !== userId)
    if (access) r.grants.push({ userId, access, ...(access === 'write' && trust ? { trustedDeviceId: session.deviceId } : {}) })
    return this.snapshot()
  }
  async createChat(sourceId?: string) {
    const { userId } = this.requireSession()
    if (sourceId) {
      const c = this.snapshot().chats.find(c => c.id === sourceId)
      if (!c || c.access === 'read') throw Error('Нет права на ответвление.')
    }
    const id = crypto.randomUUID()
    this.rooms.unshift(room(id, userId))
    return { id, workspace: this.snapshot() }
  }
  setPinned(chatId: string, pinned: boolean) {
    const { userId } = this.requireSession()
    const r = this.rooms.find(room => room.id === chatId && room.ownerId === userId)
    if (!r) throw Error('Нет права закреплять этот чат.')
    r.pinned = pinned
    return this.snapshot()
  }
  deleteChat(chatId: string) {
    const { userId } = this.requireSession()
    const idx = this.rooms.findIndex(room => room.id === chatId && room.ownerId === userId)
    if (idx < 0) throw Error('Нет права удалить этот чат.')
    this.rooms.splice(idx, 1)
    return this.snapshot()
  }
  authorize(chatId: string, target: Target, command: boolean) {
    const { userId } = this.requireSession()
    const r = this.rooms.find(r => r.id === chatId)
    const g = r?.grants.find(g => g.userId === userId)
    if (!r || (r.ownerId !== userId && g?.access !== 'write')) throw Error('Нет права отправлять сообщения в этот чат.')
    if (target.mode === 'chat') return false
    const device = this.devices.find(d => d.id === target.deviceId && d.online)
    if (!device) throw Error('Устройство недоступно.')
    if (device.ownerId === userId) return false
    if (command && g?.trustedDeviceId === device.id && r.ownerId === device.ownerId) return true
    throw Error('На этом устройстве владельца разрешены только доверенные демо-команды.')
  }
}
