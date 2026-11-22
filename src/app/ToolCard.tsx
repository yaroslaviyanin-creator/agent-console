import { Check, ChevronRight, FilePenLine, FileText, Folder, ShieldCheck, ShieldQuestion, Terminal, X } from 'lucide-react'
import { decide, useChats } from './chatStore'
import type { ToolEntry, Turn } from './chatStore'

export function ToolCard({ chatId, turn, entry }: { chatId: string; turn: Turn; entry: ToolEntry }) {
  const devices = useChats(s => s.workspace?.devices)
  const { tool, result, permission } = entry
  const path = tool.name === 'run_command' ? tool.cwd : tool.path
  const Icon = tool.name === 'run_command' ? Terminal : tool.name === 'edit_file' ? FilePenLine : FileText
  const pending = !!permission && !result && !entry.decision && turn.state === 'running'
  const status = result?.status === 'denied' ? 'Отклонено' : result?.status === 'ok' ? 'Готово'
    : result?.status === 'error' ? 'Ошибка' : turn.state === 'stopped' ? 'Остановлено'
    : pending ? 'Нужно разрешение' : entry.decision ? 'Решение принято' : 'В работе'
  if (tool.name === 'read_file' || tool.name === 'list_dir' || tool.name === 'grep_search') {
    return <details className="read-tool"><summary><ChevronRight size={14}/><Icon size={15}/><code>{tool.name}</code><span title={path}>{path.split('/').pop()}</span><small>{result?.status === 'ok' && <Check size={13}/>} {status}</small></summary>
      <pre>{path}{tool.name === 'grep_search' && '\n' + tool.pattern}{result && '\n' + result.output}</pre></details>
  }
  return <section className={'tool-card' + (pending ? ' pending' : '')}>
    <div className="tool-head"><Icon size={16}/><code>{tool.name}</code><span className={'tool-status ' + (pending ? 'amber' : '')}>{pending ? <ShieldQuestion size={13}/> : result?.status === 'ok' ? <Check size={13}/> : null}{status}</span></div>
    <div className="file-row">{tool.name === 'run_command' ? <Terminal size={14}/> : <FileText size={14}/>}<code title={path}>{path}</code>{tool.name === 'edit_file' && <span className="diff-count"><b>+{tool.diff.filter(l => l.kind === 'add').length}</b> <i>−{tool.diff.filter(l => l.kind === 'remove').length}</i></span>}</div>
    {tool.name === 'edit_file' ? <div className="diff" aria-label="Изменения файла">{tool.diff.map((line, i) => <div key={i} className={'diff-line ' + line.kind}><span aria-hidden="true">{line.kind === 'add' ? '+' : line.kind === 'remove' ? '−' : ' '}</span><code>{line.text}</code></div>)}</div>
      : <pre className="command">{tool.name === 'run_command' ? tool.command : tool.name === 'write_file' ? tool.content : ''}</pre>}
    {permission && <div className="permission"><div className="permission-info"><Folder size={14}/><span>{permission.folder}</span><span className="device-label"> · {devices?.find(d => d.id === permission.deviceId)?.name ?? permission.deviceId}</span></div>
      {pending && <div className="decisions">
        <button className="approve" disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'allow')}><Check size={14}/>Разрешить</button>
        <button disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'allow_folder')}><ShieldCheck size={14}/>Всегда для этой папки</button>
        <button disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'deny')}><X size={14}/>Отклонить</button>
      </div>}
      {entry.decision === 'allow_folder' && <p>Разрешено для этой папки</p>}
      {entry.problem && <p role="alert">{entry.problem}</p>}
    </div>}
    {result && <p className="result whitespace-pre-wrap">{result.output}</p>}
  </section>
}
