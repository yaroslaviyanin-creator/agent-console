import { CornerDownRight, RotateCcw } from 'lucide-react'
import { models } from './chatStore'
import type { Turn } from './chatStore'
import { blockReason, useAccountStore } from './accountStore'

interface Props {
  turn: Turn
  writable: boolean
  busy: boolean
  retry: (id: string) => void
}
export function RetryActions({ turn, writable, busy, retry }: Props) {
  const quotas = useAccountStore(s => s.quotas)
  const refused = turn.refusalReason !== undefined || turn.state === 'refused'
  if (turn.state === 'running' && !refused) return null
  const choices = models.filter(m => m.label !== turn.model).map(m => {
    const reason = blockReason(quotas[m.provider])
    const text = 'Повторить на ' + m.label
    return <button type="button" key={m.id} disabled={busy || !!reason} title={reason || text} onClick={() => retry(m.id)}>
      <CornerDownRight size={13}/>{text}{reason && ' · лимит'}
    </button>
  })
  if (refused) return <div className="refusal-note" role="status"><strong>Модель отказалась отвечать</strong>
    {writable && <><p>При повторе новая модель получит исходный запрос и предшествующую переписку.</p><div className="retry-choices">{choices}</div></>}
  </div>
  if (!writable) return null
  return <details className="retry-menu"><summary><RotateCcw size={13}/>Повторить на другой модели</summary><div className="retry-choices">{choices}</div></details>
}
