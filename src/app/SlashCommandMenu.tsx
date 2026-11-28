import type { PromptTemplate } from '../protocol/types'

export interface SlashMatch {
  start: number
  end: number
  query: string
}

export function findSlashQuery(text: string, cursor: number): SlashMatch | null {
  const pos = Math.max(0, Math.min(cursor, text.length))
  const before = text.slice(0, pos)
  const slashIdx = before.lastIndexOf('/')
  if (slashIdx === -1) return null
  if (slashIdx > 0 && !/\s/.test(before[slashIdx - 1]!)) return null
  const query = before.slice(slashIdx)
  if (/\s/.test(query)) return null
  return { start: slashIdx, end: pos, query }
}

export function filterPromptTemplates(templates: readonly PromptTemplate[], query: string): PromptTemplate[] {
  const q = query.trim().toLowerCase()
  if (!q || q === '/') return templates.slice(0, 12)
  return templates
    .filter(t => t.slash.toLowerCase().includes(q) || t.title.toLowerCase().includes(q.slice(1)))
    .slice(0, 12)
}

export function applySlashTemplate(text: string, match: SlashMatch, template: PromptTemplate): { nextText: string; nextCursor: number } {
  const before = text.slice(0, match.start)
  const after = text.slice(match.end)
  const inserted = template.content
  const nextText = before + inserted + after
  return { nextText, nextCursor: before.length + inserted.length }
}

export function SlashCommandMenu({
  templates,
  activeIndex,
  onHover,
  onSelect,
}: {
  templates: PromptTemplate[]
  activeIndex: number
  onHover: (idx: number) => void
  onSelect: (tpl: PromptTemplate) => void
}) {
  if (templates.length === 0) return null
  return (
    <div className="slash-menu" role="listbox" aria-label="Шаблоны команд">
      {templates.map((tpl, idx) => (
        <button
          key={tpl.id}
          type="button"
          role="option"
          aria-selected={idx === activeIndex}
          className={`slash-menu-item${idx === activeIndex ? ' is-active' : ''}`}
          onMouseDown={e => e.preventDefault()}
          onMouseEnter={() => onHover(idx)}
          onClick={() => onSelect(tpl)}
        >
          <span className="slash-menu-cmd">{tpl.slash}</span>
          <span className="slash-menu-title">{tpl.title}</span>
        </button>
      ))}
    </div>
  )
}
