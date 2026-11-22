import type { ContextMessage } from '../protocol/types'
import type { Turn } from './chatStore'

// Older demo transcripts are linear. New turns record their exact predecessor, including null at the root.
export function previousTurn(turns: Turn[], index: number): string | null {
  const parent = turns[index]?.parentId
  return parent === undefined ? turns[index - 1]?.id ?? null : parent
}

export function historyUntil(turns: Turn[], endId: string | null): ContextMessage[] {
  const chain: Turn[] = []
  const indexes = new Map(turns.map((t, i) => [t.id, i]))
  let index = endId === null ? -1 : indexes.get(endId) ?? -1
  while (index >= 0) {
    const turn = turns[index]!
    chain.push(turn)
    const parent = previousTurn(turns, index)
    const next = parent === null ? -1 : indexes.get(parent) ?? -1
    if (next >= index) throw Error('Некорректная последовательность сообщений.')
    index = next
  }
  const history: ContextMessage[] = []
  for (const t of chain.reverse()) {
    history.push({ role: 'user', text: t.prompt })
    const text = t.parts.flatMap(p => p.kind === 'text' ? [p.text] : []).join('\n')
    if (text) history.push({ role: 'assistant', text })
  }
  return history
}
