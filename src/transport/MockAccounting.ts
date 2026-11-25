import type { AccountingAdapter, AccountEvent, BillingPeriod, BillingSnapshot, MeteredProvider, Provider, Transfer } from '../protocol/types'
import { users } from './MockWorkspace'

const fiveHours = 5 * 3600_000
const sevenDays = 7 * 86400_000
const costCents = 2
const providers = ['claude', 'codex', 'paid_api'] as const
interface Usage { five: number; week: number; allowance: number }

export class MockAccounting implements AccountingAdapter {
  private usage = new Map<string, Record<MeteredProvider, Usage>>()
  private transfers: Transfer[] = []
  private ledger: BillingSnapshot
  private fiveEnd: number
  private weekEnd: number
  private demoSent = false
  private listeners = new Map<(e: AccountEvent) => void, string>()

  constructor(private currentUser: () => string | undefined, private now = Date.now, initialClaude = 18) {
    this.fiveEnd = now() + fiveHours
    this.weekEnd = now() + sevenDays
    for (const u of users) {
      this.usage.set(u.id, {
        claude: { five: u.id === 'demo' ? initialClaude : 18, week: 12, allowance: 50 },
        codex: { five: 9, week: 8, allowance: 50 },
      })
    }
    this.ledger = { current: this.period(), closed: [], hardLimitCents: 1000 }
    Object.assign(this.ledger.current.rows[0]!, { tokens: 4000, cacheTokens: 800, cents: 8 })
  }
  private period(): BillingPeriod {
    return { id: crypto.randomUUID(), openedAt: this.now(), rows: users.map(user => ({ user, tokens: 0, cacheTokens: 0, cents: 0 })) }
  }
  private user() {
    const id = this.currentUser()
    if (!id) throw Error('Войдите в аккаунт.')
    return id
  }
  private total() { return this.ledger.current.rows.reduce((sum, r) => sum + r.cents, 0) }

  refresh() {
    const time = this.now()
    const five = time >= this.fiveEnd
    const week = time >= this.weekEnd
    if (!five && !week) return
    if (five) this.fiveEnd += (1 + Math.floor((time - this.fiveEnd) / fiveHours)) * fiveHours
    if (week) this.weekEnd += (1 + Math.floor((time - this.weekEnd) / sevenDays)) * sevenDays
    for (const [id, record] of this.usage) {
      for (const provider of ['claude', 'codex'] as const) {
        if (five) Object.assign(record[provider], { five: 0, allowance: 50 })
        if (week) record[provider].week = 0
        this.notify(id, this.quota(id, provider))
      }
    }
    for (const t of this.transfers) {
      if (t.status === 'pending' && t.expiresAt <= time) { t.status = 'expired'; this.transferEvent(t) }
    }
  }
  private quota(id: string, provider: MeteredProvider | 'paid_api'): AccountEvent {
    if (provider === 'paid_api') return {
      type: 'quota_update', provider, spentUsd: this.total() / 100, hardLimitUsd: this.ledger.hardLimitCents / 100, nextRunUsd: costCents / 100,
    }
    const u = this.usage.get(id)![provider]
    return { type: 'quota_update', provider, allowancePercent: u.allowance, fiveHour: { usedPercent: u.five, resetsAt: this.fiveEnd }, sevenDay: { usedPercent: u.week, resetsAt: this.weekEnd } }
  }
  private notify(id: string, event: AccountEvent) {
    for (const [listener, owner] of this.listeners) if (owner === id && owner === this.currentUser()) listener(structuredClone(event))
  }
  private notifyBilling() {
    for (const u of users) this.notify(u.id, this.quota(u.id, 'paid_api'))
  }
  subscribe(listener: (e: AccountEvent) => void, demoDelay: number) {
    const id = this.user()
    this.refresh()
    this.listeners.set(listener, id)
    for (const p of providers) listener(this.quota(id, p))
    for (const t of this.transfers) if (t.from.id === id || t.to.id === id) listener({ type: 'quota_transfer_request', ...structuredClone(t) })
    const demo = setTimeout(() => {
      if (this.demoSent || id !== 'demo' || this.currentUser() !== id) return
      this.demoSent = true
      this.refresh()
      if (!this.transfers.some(t => t.from.id === 'alex' && t.to.id === 'demo' && t.provider === 'codex' && t.status === 'pending')) {
        this.createTransfer('alex', 'demo', 'codex', 5)
      }
    }, demoDelay)
    const interval = setInterval(() => this.refresh(), 1000)
    return () => { clearTimeout(demo); clearInterval(interval); this.listeners.delete(listener) }
  }
  // The mock debits one accepted invocation before streaming, including runs stopped later.
  charge(provider: Provider, prompt: string): string | undefined {
    if (provider === 'antigravity') return undefined
    const id = this.user()
    this.refresh()
    if (provider === 'paid_api') {
      if (this.total() + costCents > this.ledger.hardLimitCents) return 'Следующий запрос превысит жёсткий лимит API.'
      const row = this.ledger.current.rows.find(r => r.user.id === id)!
      row.cents += costCents
      row.tokens += 100 + Math.ceil(prompt.length / 4)
      row.cacheTokens += 24
      this.notifyBilling()
    } else {
      const u = this.usage.get(id)![provider]
      if (u.five >= u.allowance || u.week >= 100) return 'Достигнут лимит провайдера.'
      u.five++
      u.week = Math.min(100, Math.round((u.week + 0.1) * 10) / 10)
      this.notify(id, this.quota(id, provider))
    }
  }
  private transferEvent(t: Transfer) {
    const event = { type: 'quota_transfer_request' as const, ...t }
    this.notify(t.to.id, event); this.notify(t.from.id, event)
  }
  private createTransfer(from: string, to: string, provider: MeteredProvider, percent: number) {
    const sender = users.find(u => u.id === from)
    const donor = users.find(u => u.id === to)
    if (!sender || !donor || from === to || !Number.isInteger(percent) || percent < 1 || percent > 50) throw Error('Выберите другого пользователя и от 1 до 50%.')
    if (this.transfers.some(t => t.from.id === from && t.to.id === to && t.provider === provider && t.status === 'pending')) throw Error('Запрос этому пользователю уже отправлен.')
    const t: Transfer = { requestId: crypto.randomUUID(), from: sender, to: donor, provider, percent, expiresAt: this.fiveEnd, status: 'pending' }
    this.transfers.push(t); this.transferEvent(t)
  }
  async requestTransfer(to: string, provider: MeteredProvider, percent: number) {
    this.refresh()
    this.createTransfer(this.user(), to, provider, percent)
  }
  async resolveTransfer(id: string, accept: boolean) {
    this.refresh()
    const t = this.transfers.find(t => t.requestId === id && t.to.id === this.user())
    if (!t || t.status !== 'pending') throw Error('Запрос уже закрыт или недоступен.')
    if (accept) {
      const donor = this.usage.get(t.to.id)![t.provider]
      const recipient = this.usage.get(t.from.id)![t.provider]
      if (donor.allowance - donor.five < t.percent || recipient.allowance + t.percent > 100) throw Error('Недостаточно свободного лимита для передачи.')
      donor.allowance -= t.percent; recipient.allowance += t.percent
      this.notify(t.to.id, this.quota(t.to.id, t.provider)); this.notify(t.from.id, this.quota(t.from.id, t.provider))
    }
    t.status = accept ? 'accepted' : 'declined'
    this.transferEvent(t)
  }
  async billing() { this.user(); return structuredClone(this.ledger) }
  async setHardLimit(cents: number) {
    this.user()
    if (!Number.isSafeInteger(cents) || cents < 0) throw Error('Введите сумму в долларах с точностью до цента.')
    this.ledger.hardLimitCents = cents; this.notifyBilling()
    return this.billing()
  }
  async closePeriod(id: string) {
    this.user()
    if (this.ledger.current.id !== id) throw Error('Этот расчёт уже закрыт.')
    if (this.total() === 0) throw Error('Нет расходов для закрытия.')
    this.ledger.closed.unshift({ ...this.ledger.current, closedAt: this.now() })
    this.ledger.current = this.period(); this.notifyBilling()
    return this.billing()
  }
}
