import { useEffect, useState } from 'react'
import { ArrowLeft } from 'lucide-react'
import type { BillingPeriod, BillingSnapshot } from '../protocol/types'
import { useAccountStore } from './accountStore'
import { dollars } from './Quotas'
import { transport } from './runtime'

const total = (p: BillingPeriod) => p.rows.reduce((sum,r) => sum + r.cents,0)
function Charges({ period }: { period: BillingPeriod }) {
  return <div className="charges"><table><thead><tr><th>Пользователь</th><th>Токены</th><th>Кэш-токены</th><th>Сумма</th></tr></thead><tbody>{period.rows.map(r => <tr key={r.user.id}><th>{r.user.name}</th><td>{r.tokens.toLocaleString('ru')}</td><td>{r.cacheTokens.toLocaleString('ru')}</td><td>{dollars(r.cents)}</td></tr>)}</tbody><tfoot><tr><th colSpan={3}>Итого</th><td>{dollars(total(period))}</td></tr></tfoot></table></div>
}
export function Billing({ back }: { back: () => void }) {
  const quota = useAccountStore(s => s.quotas.paid_api)
  const [data, setData] = useState<BillingSnapshot | null>(null)
  const [limit, setLimit] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    let alive = true
    transport.accounting.billing().then(v => { if (alive) setData(v) }).catch(e => { if (alive) setNotice(String(e)) })
    return () => { alive = false }
  }, [quota])
  useEffect(() => { if (data) setLimit((data.hardLimitCents / 100).toFixed(2)) }, [data?.hardLimitCents])
  return <section className="billing-view"><button className="back-action" onClick={back}><ArrowLeft size={15}/>К чату</button><h1>Биллинг API</h1>
    {data && <><div className="billing-section"><h2>Текущий расчёт</h2><Charges period={data.current}/><button className="alt-action" disabled={!total(data.current)} onClick={async () => {
      try { setData(await transport.accounting.closePeriod(data.current.id)); setNotice('Расчёт закрыт и сохранён в истории.') } catch (e) { setNotice(String(e)) }
    }}>Расчёт закрыт</button></div>
    <form className="billing-section limit-form" onSubmit={async e => {
      e.preventDefault()
      try { setData(await transport.accounting.setHardLimit(Math.round(Number(limit) * 100))); setNotice('Лимит сохранён.') } catch (e) { setNotice(String(e)) }
    }}><label>Жёсткий лимит на расчёт, $<input type="number" step="0.01" min="0" required value={limit} onChange={e => setLimit(e.target.value)}/></label><button className="alt-action">Сохранить лимит</button>
    <small>Общий для всех пользователей. Один принятый демо-запрос стоит $0.02.</small></form>
    {!!data.closed.length && <div className="billing-section"><h2>Закрытые расчёты</h2>{data.closed.map(p => <details key={p.id}><summary>{new Date(p.closedAt).toLocaleString('ru')} · {dollars(total(p))}</summary><Charges period={p}/></details>)}</div>}</>}
    <p role="status">{notice}</p></section>
}
