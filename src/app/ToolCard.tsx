import { useEffect, useState } from 'react'
import { AlertCircle, Check, ChevronRight, FilePenLine, FileText, Folder, LoaderCircle, ShieldCheck, ShieldQuestion, Terminal, X } from 'lucide-react'
import { decide, useChats } from './chatStore'
import type { ToolEntry, Turn } from './chatStore'

type StatusTone = 'running' | 'pending' | 'ok' | 'error' | 'stopped'

export function ToolCard({ chatId, turn, entry }: { chatId: string; turn: Turn; entry: ToolEntry }) {
  const devices = useChats(s => s.workspace?.devices)
  const { tool, result, permission } = entry
  const path = tool.name === 'run_command' ? tool.cwd : tool.path
  const Icon = tool.name === 'run_command' ? Terminal : tool.name === 'edit_file' ? FilePenLine : FileText
  const pending = !!permission && !result && !entry.decision && turn.state === 'running'
  const status = result?.status === 'denied' ? 'Отклонено' : result?.status === 'ok' ? 'Готово'
    : result?.status === 'error' ? 'Ошибка' : turn.state === 'stopped' ? 'Остановлено'
    : pending ? 'Ждёт подтверждения' : entry.decision ? 'Выполняется' : 'Выполняется'
  const tone: StatusTone = result?.status === 'denied' || result?.status === 'error' ? 'error'
    : result?.status === 'ok' ? 'ok'
    : pending ? 'pending'
    : turn.state === 'stopped' ? 'stopped'
    : 'running'

  const [open, setOpen] = useState(pending || !result)
  useEffect(() => {
    if (pending) setOpen(true)
  }, [pending])

  const statusBadge = (
    <span className={`tool-status tone-${tone}`}>
      {tone === 'pending' && <ShieldQuestion size={12} />}
      {tone === 'ok' && <Check size={12} />}
      {tone === 'error' && <AlertCircle size={12} />}
      {tone === 'running' && <LoaderCircle size={12} className="spin" />}
      {status}
    </span>
  )

  if (tool.name === 'read_file' || tool.name === 'list_dir' || tool.name === 'grep_search') {
    return (
      <details className="tool-glass-card read-tool">
        <summary>
          <ChevronRight size={14} />
          <Icon size={14} />
          <code>{tool.name}</code>
          <span title={path}>{path.split('/').pop()}</span>
          {statusBadge}
        </summary>
        <pre>{path}{tool.name === 'grep_search' && '\n' + tool.pattern}{result && '\n' + result.output}</pre>
      </details>
    )
  }

  const addCount = tool.name === 'edit_file' ? tool.diff.filter(l => l.kind === 'add').length : 0
  const removeCount = tool.name === 'edit_file' ? tool.diff.filter(l => l.kind === 'remove').length : 0

  return (
    <details
      className={`tool-glass-card tool-card${pending ? ' pending' : ''}`}
      open={open}
      onToggle={e => setOpen(e.currentTarget.open)}
    >
      <summary className="tool-head">
        <ChevronRight size={14} />
        <Icon size={15} />
        <code>{tool.name}</code>
        <span className="tool-summary-path" title={path}>{path.split('/').pop() || path}</span>
        {statusBadge}
      </summary>
      <div className="tool-card-body">
        <div className="file-row">
          {tool.name === 'run_command' ? <Terminal size={13} /> : <FileText size={13} />}
          <code title={path}>{path}</code>
          {tool.name === 'edit_file' && (
            <span className="diff-count"><b>+{addCount}</b> <i>−{removeCount}</i></span>
          )}
        </div>
        {tool.name === 'edit_file' ? (
          <div className="diff" aria-label="Изменения файла">
            {tool.diff.map((line, i) => (
              <div key={i} className={'diff-line ' + line.kind}>
                <span aria-hidden="true">{line.kind === 'add' ? '+' : line.kind === 'remove' ? '−' : ' '}</span>
                <code>{line.text}</code>
              </div>
            ))}
          </div>
        ) : (
          <pre className="command">{tool.name === 'run_command' ? tool.command : tool.name === 'write_file' ? tool.content : ''}</pre>
        )}
        {permission && (
          <div className="permission">
            <div className="permission-info">
              <Folder size={13} />
              <span>{permission.folder}</span>
              <span className="device-label"> · {devices?.find(d => d.id === permission.deviceId)?.name ?? permission.deviceId}</span>
            </div>
            {pending && (
              <div className="decisions">
                <button type="button" className="approve" disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'allow')}>
                  <Check size={14} />Разрешить
                </button>
                <button type="button" disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'allow_folder')}>
                  <ShieldCheck size={14} />Всегда для этой папки
                </button>
                <button type="button" className="reject" disabled={entry.sending} onClick={() => void decide(chatId, turn.id, entry, 'deny')}>
                  <X size={14} />Отклонить
                </button>
              </div>
            )}
            {entry.decision === 'allow_folder' && <p>Разрешено для этой папки</p>}
            {entry.problem && <p role="alert">{entry.problem}</p>}
          </div>
        )}
        {result && <p className="result whitespace-pre-wrap">{result.output}</p>}
      </div>
    </details>
  )
}
