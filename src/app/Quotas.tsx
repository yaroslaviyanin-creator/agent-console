import { type Ref, useEffect, useState } from 'react'
import { Activity, ArrowLeftRight } from 'lucide-react'
import type { MeteredProvider, Provider, Transfer, Workspace } from '../protocol/types'
import { useAccountStore, blockReason } from './accountStore'
import { transport } from './runtime'

const label: Record<Provider, string> = { claude: 'Claude', codex: 'Codex', paid_api: 'API', gemini: 'Gemini', antigravity: 'Antigravity' }
const transferState = { pending: 'ожидает ответа', accepted: 'одобрен', declined: 'отклонён', expired: 'истёк' }
export const dollars = (cents: number) => '$' + (cents / 100).toFixed(2)
export const limitDollars = (cents: number) => cents % 100 === 0 ? '$' + cents / 100 : dollars(cents)
const percent = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 2, style: 'percent' })
export const quotaPercent = (value: number) => percent.format(value / 100)
function remaining(end: number, now: number) {
  const mins = Math.max(0, Math.ceil((end - now) / 60_000))
  const hours = Math.floor(mins / 60)
  return hours >= 24 ? Math.floor(hours / 24) + ' д ' + hours % 24 + ' ч' : hours + ' ч ' + mins % 60 + ' мин'
}
function ProviderQuotas() {
  const quotas = useAccountStore(s => s.quotas)
  const [now, setNow] = useState(Date.now)
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 10_000); return () => clearInterval(timer) }, [])
  return <section className="provider-quotas" aria-label="Лимиты Claude и Codex">
    {(['claude', 'codex'] as const).map(p => {
      const q = quotas[p]
      if (!q || q.provider === 'paid_api') return null
      return <div key={p} className="quota-provider"><h3>{label[p]}<small>Порог {q.allowancePercent}%</small></h3>
        {([['5 ч', q.fiveHour], ['7 д', q.sevenDay]] as const).map(([name,v]) => {
          const reset = remaining(v.resetsAt,now)
          return <div className="quota-window" key={name} title={'Сброс через ' + reset}>
            <span>{name}</span><progress aria-label={label[p] + ' за ' + name} value={v.usedPercent} max="100"/><b>{v.usedPercent}%</b><small>Сброс через {reset}</small>
          </div>
        })}</div>
    })}
  </section>
}
export function Quotas({ userId, menuRef, onOpenChange }: { userId: string; menuRef: Ref<HTMLDetailsElement>; onOpenChange: (open: boolean) => void }) {
  const { quotas, requests } = useAccountStore()
  const api = quotas.paid_api
  return <details className="quota-menu" ref={menuRef} onToggle={e => onOpenChange(e.currentTarget.open)}><summary><Activity size={14}/>Лимиты{api?.provider === 'paid_api' && <small>API {dollars(Math.round(api.spentUsd * 100))}</small>}</summary>
    <section><ProviderQuotas/>
    {requests.length > 0 && <div className="transfer-log"><h3>Обмен лимитами</h3>{requests.slice(-5).reverse().map(r => <p key={r.requestId}>{r.from.id === userId ? 'Запрос к ' + r.to.name : 'Запрос от ' + r.from.name} · {r.percent}% {label[r.provider]} · {transferState[r.status]}</p>)}</div>}
    </section></details>
}

export function ServerQuotas({ menuRef, onOpenChange }: { menuRef: Ref<HTMLDetailsElement>; onOpenChange: (open: boolean) => void }) {
  const quota = useAccountStore(s => s.serverQuota)
  const [now, setNow] = useState(Date.now)
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 10_000); return () => clearInterval(timer) }, [])
  const windows = quota ? [
    { name: '5ч', window: quota.fiveHour, bypass: quota.bypass5hLimit },
    { name: '7д', window: quota.sevenDay, bypass: false },
  ] : []
  return <details className="quota-menu server-quotas" ref={menuRef} onToggle={e => onOpenChange(e.currentTarget.open)}>
    <summary aria-label="Лимиты за 5 часов и 7 дней">
      <Activity size={14} aria-hidden="true"/>
      {windows.length ? windows.map(({ name, window, bypass }) =>
        <span key={name}>{name} <b>{bypass ? 'Отключён' : quota?.planTier === 'unlimited' ? '∞' : quotaPercent(window.usedPercent)}</b></span>
      ) : 'Лимиты'}
    </summary>
    <section aria-label="Расход по тарифу">
      {!quota && <p>Данные о лимитах пока недоступны.</p>}
      {windows.map(({ name, window, bypass }) => <div className="server-quota-window" key={name}>
        <strong>{name}</strong><span>{dollars(window.usedCents)}{window.limitCents > 0 && ' / ' + limitDollars(window.limitCents)}</span>
        {window.limitCents > 0 && !bypass && <progress aria-label={'Расход за ' + name} value={window.usedPercent} max={100}/>}
        <small>{bypass ? 'Лимит отключён' : quota?.planTier === 'unlimited' ? 'Без ограничений' : quota?.planTier === 'none' ? 'Без тарифа' : window.resetsAt > now ? 'Ближайшее снижение расхода через ' + remaining(window.resetsAt, now) : ''}</small>
      </div>)}
    </section>
  </details>
}

export function Incoming({ request }: { request: Transfer }) {
  const [error, setError] = useState('')
  async function resolve(accept: boolean) {
    try { await transport.accounting.resolveTransfer(request.requestId, accept) }
    catch (e) { setError(e instanceof Error ? e.message : 'Запрос недоступен.') }
  }
  return <aside className="transfer-toast" aria-label="Входящий запрос лимита"><ArrowLeftRight size={16}/><div><strong>{request.from.name} просит {request.percent}% {label[request.provider]}</strong><small>До {new Date(request.expiresAt).toLocaleTimeString('ru',{hour:'2-digit',minute:'2-digit'})} · текущее окно 5 ч</small>{error && <small role="alert">{error}</small>}</div><button onClick={() => void resolve(true)}>Отдать</button><button onClick={() => void resolve(false)}>Отклонить</button></aside>
}

function RequestForm({ w, provider }: { w: Workspace; provider: MeteredProvider }) {
  const others = w.users.filter(u => u.id !== w.user.id)
  const [to, setTo] = useState(others[0]!.id)
  const [percent, setPercent] = useState('5')
  const [message, setMessage] = useState('')
  const requests = useAccountStore(s => s.requests)
  const pending = requests.find(r => r.from.id === w.user.id && r.to.id === to && r.provider === provider && r.status === 'pending')
  return <details className="request-form"><summary>Попросить +% у друга</summary><form onSubmit={async e => {
    e.preventDefault()
    try { await transport.accounting.requestTransfer(to,provider,Number(percent)); setMessage('') }
    catch (e) { setMessage(e instanceof Error ? e.message : 'Ошибка отправки.') }
  }}><label>У кого<select value={to} onChange={e => { setTo(e.target.value); setMessage('') }}>{others.map(u => <option key={u.id} value={u.id}>{u.name}</option>)}</select></label>
    <label>Процент<input type="number" min="1" max="50" step="1" required value={percent} onChange={e => setPercent(e.target.value)}/></label>
    <button disabled={!!pending}>Отправить запрос</button><small role="status">{pending ? 'Запрос отправлен. Ожидается ответ.' : message || 'До конца текущего окна 5 ч'}</small></form></details>
}

export function LimitActions({ w, provider, reason, switchCodex, billing }: { w: Workspace; provider: Provider; reason: string; switchCodex: () => void; billing: () => void }) {
  const quotas = useAccountStore(s => s.quotas)
  const q = quotas[provider]
  return <div className="limit-actions"><strong>{label[provider]} · {reason}</strong>
    {provider !== 'paid_api' && provider !== 'gemini' && provider !== 'antigravity' && q && q.provider !== 'paid_api' && q.sevenDay.usedPercent < 100 && <RequestForm w={w} provider={provider}/>}
    {provider === 'paid_api' && <button onClick={billing}>Открыть биллинг API</button>}
    {provider !== 'codex' && !blockReason(quotas.codex) && <button onClick={switchCodex}>Продолжить этот чат в Codex</button>}
  </div>
}
