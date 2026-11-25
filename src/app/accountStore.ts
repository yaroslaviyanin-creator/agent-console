import { create } from 'zustand'
import type { AccountEvent, Provider, ServerQuota } from '../protocol/types'

type Quota = Extract<AccountEvent, { type: 'quota_update' }>
type TransferRequest = Extract<AccountEvent, { type: 'quota_transfer_request' }>

interface AccountState {
  serverQuota: ServerQuota | null
  quotas: Partial<Record<Provider, Quota>>
  requests: TransferRequest[]
  receive: (event: AccountEvent) => void
}

export const useAccountStore = create<AccountState>(set => ({
  serverQuota: null,
  quotas: {},
  requests: [],
  receive: event => set(state => event.type === 'quota_update'
    ? { quotas: { ...state.quotas, [event.provider]: event } }
    : { requests: [...state.requests.filter(t => t.requestId !== event.requestId), event] }),
}))

export function blockReason(q: Quota | undefined) {
  if (!q) return ''
  if (q.provider === 'paid_api') return Math.round((q.spentUsd + q.nextRunUsd) * 100) > Math.round(q.hardLimitUsd * 100) ? 'Следующий запрос превысит жёсткий лимит API.' : ''
  if (q.sevenDay.usedPercent >= 100) return 'Недельный лимит исчерпан.'
  return q.fiveHour.usedPercent >= q.allowancePercent ? 'Достигнут порог ' + q.allowancePercent + '% за 5 часов.' : ''
}
