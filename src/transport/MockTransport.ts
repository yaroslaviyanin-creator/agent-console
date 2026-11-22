import type { MockProject } from './MockProject'
import { MockAccounting } from './MockAccounting'
import { MockWorkspace } from './MockWorkspace'
import type {
  AccountEvent, PermissionDecision, RunEvent, RunRequest, TransportAdapter,
} from '../protocol/types'

type Scenario = 'normal' | 'refusal' | 'error' | 'quota'
type Pending = { deviceId: string; resolve: (decision: PermissionDecision) => void; signal: AbortSignal }

function delay(ms: number, signal: AbortSignal) {
  signal.throwIfAborted()
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, ms)
    function abort() {
      clearTimeout(timer)
      reject(signal.reason)
    }
    signal.addEventListener('abort', abort, { once: true })
  })
}

export class MockTransport implements TransportAdapter {
  private readonly pending = new Map<string, Pending>()
  private readonly projects = new Map<string, MockProject>()
  private readonly grants = new Set<string>()
  private accepted = new Set<string>()
  private epoch = 0
  readonly workspace = new MockWorkspace(deviceId => {
    if (!deviceId) {
      this.epoch++
      this.grants.clear()
    } else {
      for (const key of this.grants) if (JSON.parse(key)[1] === deviceId) this.grants.delete(key)
    }
    for (const [id, pending] of this.pending) {
      if (deviceId && pending.deviceId !== deviceId) continue
      pending.resolve('deny')
      this.pending.delete(id)
    }
  })
  readonly accounting: MockAccounting

  constructor(private readonly options: { delayMs?: number; scenario?: Scenario; nearLimit?: boolean; now?: () => number } = {}) {
    this.accounting = new MockAccounting(() => this.workspace.userId, options.now, options.scenario === 'quota' ? 49 : options.nearLimit ? 48 : 18)
  }

  subscribeAccount(listener: (event: AccountEvent) => void) {
    return this.accounting.subscribe(listener, (this.options.delayMs ?? 120) * 45)
  }

  respondToPermission(requestId: string, decision: PermissionDecision) {
    const pending = this.pending.get(requestId)
    if (!pending || pending.signal.aborted) return false
    this.pending.delete(requestId)
    pending.resolve(decision)
    return true
  }

  async *stream(request: RunRequest, signal: AbortSignal): AsyncGenerator<RunEvent> {
    const { chatId, messageId, target, model } = request
    let seq = 0
    const envelope = () => ({ chatId, messageId, seq: ++seq })
    const epoch = this.epoch
    const mcp = target.mode === 'mcp' ? await import('./MockProject') : undefined
    if (epoch !== this.epoch) throw Error('Сессия изменилась.')
    const intent = mcp?.parseDemoIntent(request.prompt)
    const checkTests = intent?.name === 'run_command'
    const trusted = this.workspace.authorize(chatId, target, checkTests)
    signal.throwIfAborted()
    const runKey = JSON.stringify([this.workspace.userId, chatId, messageId])
    if (this.accepted.has(runKey)) {
      yield { ...envelope(), type: 'error', code: 'duplicate_run', message: 'Этот запрос уже принят.', retryable: false }
      return
    }
    const blocked = this.accounting.charge(model.provider, request.prompt)
    if (blocked) {
      yield { ...envelope(), type: 'error', code: 'limit_reached', message: blocked, retryable: true }
      return
    }
    this.accepted.add(runKey)
    const tick = async () => {
      await delay(this.options.delayMs ?? 120, signal)
      if (epoch !== this.epoch) throw Error('Доступ или сессия изменились.')
      this.workspace.authorize(chatId, target, checkTests)
    }
    const scenario = this.options.scenario ?? 'normal'
    await tick()
    for (const delta of ['Изучаю запрос. ', 'Готовлю ответ.']) {
      yield { ...envelope(), type: 'thinking_delta', delta }
      await tick()
    }

    if (scenario === 'error') {
      yield { ...envelope(), type: 'error', code: 'mock_unavailable', message: 'Демо-агент недоступен.', retryable: true }
      return
    }
    const sample = request.prompt === 'Объясни на примере, что такое тайм-аут подключения.'
    if (scenario === 'refusal' || (sample && model.provider === 'claude')) {
      yield { ...envelope(), type: 'text_delta', delta: 'Я не могу выполнить этот запрос.' }
      await tick()
      yield { ...envelope(), type: 'refusal_detected', reason: 'Демонстрационный отказ модели' }
      yield { ...envelope(), type: 'done', reason: 'refused' }
      return
    }

    let answer = 'Выберите пример в меню «Сценарии MCP» под полем сообщения.'
    if (mcp && intent && target.mode === 'mcp') {
      const { MockProject, projectFolder } = mcp
      const project = this.projects.get(target.deviceId) ?? new MockProject()
      this.projects.set(target.deviceId, project)
      for (const step of project.steps(intent)) {
        const tool = step.tool
        const callId = crypto.randomUUID()
        yield { ...envelope(), type: 'tool_use', callId, tool }
        await tick()
        const grantKey = JSON.stringify([chatId, target.deviceId, projectFolder, tool.name])
        if (step.approval && !trusted && !this.grants.has(grantKey)) {
          const requestId = crypto.randomUUID()
          let finish!: (decision: PermissionDecision) => void
          const decision = new Promise<PermissionDecision>(resolve => { finish = resolve })
          const abort = () => { this.pending.delete(requestId); finish('deny') }
          this.pending.set(requestId, { resolve: finish, signal, deviceId: target.deviceId })
          signal.addEventListener('abort', abort, { once: true })
          try {
            yield { ...envelope(), type: 'permission_request', requestId, callId, deviceId: target.deviceId, folder: projectFolder, tool }
            const answer = await decision
            signal.throwIfAborted()
            if (answer === 'deny') {
              yield { ...envelope(), type: 'tool_result', callId, status: 'denied', output: 'Пользователь отклонил операцию.' }
              yield { ...envelope(), type: 'done', reason: 'denied' }
              return
            }
            if (answer === 'allow_folder') this.grants.add(grantKey)
          } finally {
            this.pending.delete(requestId)
            signal.removeEventListener('abort', abort)
          }
        }
        await tick()
        const result = step.perform()
        yield { ...envelope(), type: 'tool_result', callId, status: result.ok ? 'ok' : 'error', output: result.output }
        if (!result.ok) {
          yield { ...envelope(), type: 'error', code: 'mock_tool_failed', message: result.output, retryable: true }
          return
        }
        answer = result.answer
      }
    }
    const chunks = target.mode === 'mcp'
      ? answer.match(/[\s\S]{1,48}/gu) ?? [answer]
      : sample ? ['Тайм-аут подключения — это время, которое клиент ждёт установления соединения. ', 'Например, при лимите **3 секунды** клиент прекратит попытку, если за это время соединение не установлено.']
      : ['Получен запрос ', '«' + request.prompt + '».\n\n', 'Ответ передан ', '**демонстрационным агентом**.']
    for (const delta of chunks) {
      await tick()
      yield { ...envelope(), type: 'text_delta', delta }
    }
    yield { ...envelope(), type: 'done', reason: 'completed' }
  }
}
