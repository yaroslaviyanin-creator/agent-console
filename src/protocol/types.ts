// Wire payloads contain only JSON values. Timestamps are Unix milliseconds.
export type Provider = 'antigravity' | 'codex' | 'claude' | 'paid_api'
export type ShareAccess = 'read' | 'fork' | 'write'
export type Target = { mode: 'chat' } | { mode: 'mcp'; deviceId: string }

export interface ContextMessage { role: 'user' | 'assistant'; text: string }
export interface RunRequest {
  history?: ContextMessage[] // Ordered conversation before the current prompt.
  retryOf?: string
  chatId: string
  messageId: string // ID of the new assistant message
  model: { provider: Provider; id: string }
  reasoning?: string
  target: Target
  prompt: string
}

export type DiffLine = { kind: 'context' | 'remove' | 'add'; text: string }
export type ToolCall =
  | { name: 'read_file' | 'list_dir'; path: string }
  | { name: 'edit_file'; path: string; diff: DiffLine[] }
  | { name: 'write_file'; path: string; content: string }
  | { name: 'grep_search'; path: string; pattern: string }
  | { name: 'run_command'; cwd: string; command: string }

export type PermissionDecision = 'allow' | 'allow_folder' | 'deny'

type RunPayload =
  | { type: 'started'; prompt: string; model: string; createdAt: number }
  | { type: 'thinking_delta' | 'text_delta'; delta: string }
  | { type: 'tool_use'; callId: string; tool: ToolCall }
  | { type: 'permission_request'; requestId: string; callId: string; folder: string; deviceId: string; tool: ToolCall }
  | { type: 'tool_result'; callId: string; status: 'ok' | 'denied' | 'error'; output: string }
  | { type: 'refusal_detected'; reason: string }
  | { type: 'done'; reason: 'completed' | 'refused' | 'denied'; durationMs?: number }
  | { type: 'error'; code: string; message: string; retryable: boolean }

export type RunEvent = RunPayload & { chatId: string; messageId: string; seq: number }

export interface QuotaWindow {
  usedPercent: number
  resetsAt: number
}
export type MeteredProvider = 'claude' | 'codex'
export interface Transfer {
  requestId: string
  from: Member
  to: Member
  provider: MeteredProvider
  percent: number
  expiresAt: number
  status: 'pending' | 'accepted' | 'declined' | 'expired'
}
export interface BillingRow { user: Member; tokens: number; cacheTokens: number; cents: number }
export interface BillingPeriod { id: string; openedAt: number; rows: BillingRow[] }
export interface BillingSnapshot {
  current: BillingPeriod
  closed: (BillingPeriod & { closedAt: number })[]
  hardLimitCents: number
}
export interface AccountingAdapter {
  requestTransfer(to: string, provider: MeteredProvider, percent: number): Promise<void>
  resolveTransfer(id: string, accept: boolean): Promise<void>
  billing(): Promise<BillingSnapshot>
  setHardLimit(cents: number): Promise<BillingSnapshot>
  closePeriod(id: string): Promise<BillingSnapshot>
}
export type AccountEvent =
  | { type: 'quota_update'; provider: 'claude' | 'codex'; allowancePercent: number; fiveHour: QuotaWindow; sevenDay: QuotaWindow }
  | { type: 'quota_update'; provider: 'paid_api'; spentUsd: number; hardLimitUsd: number; nextRunUsd: number }
  | ({ type: 'quota_transfer_request' } & Transfer)

export type ProtocolEvent = RunEvent | AccountEvent

export interface TransportAdapter {
  accounting: AccountingAdapter
  workspace: WorkspaceAdapter
  admin: AdminAdapter
  // One ordered stream per message. done/error are terminal; cancellation rejects with AbortError.
  stream(request: RunRequest, signal: AbortSignal): AsyncIterable<RunEvent>
  // False for an unknown, expired or already handled request.
  respondToPermission(requestId: string, decision: PermissionDecision): boolean | Promise<boolean>
  subscribeAccount(listener: (event: AccountEvent) => void): () => void
}

export interface LoginRequest { connection: string; login: string; password: string; deviceName: string }
export interface Member { id: string; name: string; role?: UserRole; isActive?: boolean }
export interface Device { id: string; ownerId: string; name: string; online: boolean; lastSeenAt?: number }
export interface Grant { userId: string; access: ShareAccess; trustedDeviceId?: string }
export interface ChatAccess {
  id: string
  ownerId: string
  access: ShareAccess | 'owner'
  grants: Grant[] // Only owners receive the recipient list.
  trustedDeviceId?: string
}
export interface Workspace {
  user: Member
  deviceId: string
  users: Member[]
  devices: Device[]
  chats: ChatAccess[]
}
export interface WorkspaceAdapter {
  login(request: LoginRequest): Promise<Workspace>
  logout(): void | Promise<void>
  revokeDevice(id: string): Promise<Workspace | null>
  setShare(chatId: string, userId: string, access: ShareAccess | null, trust: boolean): Promise<Workspace>
  createChat(sourceId?: string): Promise<{ id: string; workspace: Workspace }>
}

// REST payloads used by the server transport. Chat provider identifies the server
// backend, while RunRequest.model.provider identifies the client model family.
export type ServerProvider = 'stub' | 'antigravity' | 'codex' | 'claude' | 'paid_api'
export interface ServerModel {
  id: string
  name: string
  isDefault: boolean
  reasoningEfforts?: string[]
  defaultReasoning?: string
}
export interface ServerHealth { status: 'ok'; provider: ServerProvider }
export interface ServerLoginRequest { username: string; password: string; deviceName: string }
export type UserRole = 'pending' | 'user' | 'admin'
export type PlanTier = 'none' | 'pro' | '2x_pro' | 'unlimited'
export interface Subscription {
  planTier: PlanTier
  bypass5hLimit: boolean
  subscriptionUntil: number
}
export interface ServerQuotaWindow extends QuotaWindow {
  usedCents: number
  limitCents: number
}
export interface ServerQuota extends Subscription {
  fiveHour: ServerQuotaWindow
  sevenDay: ServerQuotaWindow
}
export interface ServerDirectoryUser { id: string; username: string }
export interface ServerUser extends ServerDirectoryUser, Partial<Subscription> { role?: UserRole; isActive?: boolean }
export interface AdminUser extends ServerUser {
  role: UserRole
  isActive: boolean
  devicesCount: number
  chatsCount: number
  createdAt: number
  planTier: PlanTier
  bypass5hLimit: boolean
  subscriptionUntil: number
  usage5h: number
  usage7d: number
  limit5h: number
  limit7d: number
  resetsAt5h: number
  resetsAt7d: number
}
export interface CreateAdminUser extends Partial<Subscription> { username: string; password: string; role: UserRole }
export interface UpdateAdminUser extends Partial<Subscription> { role?: UserRole; isActive?: boolean; newPassword?: string }
export interface AdminAdapter {
  list(signal?: AbortSignal): Promise<AdminUser[]>
  create(user: CreateAdminUser): Promise<AdminUser>
  update(id: string, changes: UpdateAdminUser): Promise<void>
  revokeDevices(id: string): Promise<void>
}
export interface ServerAdminAdapter extends AdminAdapter {
  chats(userId: string, signal?: AbortSignal): Promise<AdminChat[]>
  messages(userId: string, chatId: string, signal?: AbortSignal): Promise<ServerMessage[]>
}
export interface ServerDevice { id: string; userId: string; name: string; lastSeenAt: number; createdAt: number; isCurrent?: boolean }
export interface ServerSession { user: ServerUser; device: ServerDevice }
export interface ServerLoginResponse extends ServerSession { token: string }
export interface ServerCreateChatRequest { title?: string; model?: string }
export interface ServerChat {
  ownerId?: string
  access?: ChatAccess['access']
  grants?: Grant[]
  id: string
  title: string
  provider: ServerProvider
  model: string
  createdAt: number
  updatedAt: number
}
export interface AdminChat extends ServerChat { messagesCount: number }
export interface IndexedCodexChat extends ServerChat { archived: boolean }
export interface MessageAttachment {
  kind: 'file' | 'image'
  name: string
  path?: string
}
export interface ServerMessage {
  attachments?: MessageAttachment[]
  progress?: string
  durationMs?: number
  id: string
  runId?: string
  chatId: string
  role: ContextMessage['role']
  content: string
  thinking: string
  createdAt: number
}
export interface ServerError { code: string; message: string }

export interface UserProfile {
  displayName: string
  profileMd: string
  memoryMd: string
  memoryEnabled: boolean
  memoryUpdatedAt: number
}

export type UserProfilePatch = Partial<Omit<UserProfile, 'memoryUpdatedAt'>>
