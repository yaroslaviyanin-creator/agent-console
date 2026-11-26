import { Receipt, CodeXml, Eye, GitFork, MessageSquare, Plus, Radio, RefreshCw, Settings2, Users, X } from 'lucide-react'
import { newChat, refreshCodexIndex, selectChat, useChats } from './chatStore'
import type { Chat } from './chatStore'
import { useAccountStore } from './accountStore'
import { dollars, quotaPercent } from './Quotas'

export function Sidebar({
  close,
  openBilling,
  openSettings,
}: {
  close: () => void
  openBilling: () => void
  openSettings: () => void
}) {
  const { chats, activeId, mode, connected, codexIndex, workspace } = useChats()
  const serverQuota = useAccountStore(s => s.serverQuota)
  const paidApiQuota = useAccountStore(s => s.quotas.paid_api)
  const online = mode === 'demo' || connected

  function row(c: Chat) {
    const Icon = c.access === 'read' ? Eye : c.access === 'fork' ? GitFork : c.access === 'write' ? Users : MessageSquare
    return (
      <button
        key={c.id}
        className={'chat-link ' + (c.id === activeId ? 'selected' : '')}
        aria-current={c.id === activeId ? 'page' : undefined}
        title={c.title}
        onClick={() => { selectChat(c.id); close() }}
      >
        <Icon size={15} aria-label={{ read: 'Чтение', fork: 'Разветвление', write: 'Полный доступ', owner: 'Мой чат' }[c.access]} />
        <span>{c.title}</span>
        {c.archived && <small className="archive-label">Архив</small>}
      </button>
    )
  }

  const shared = chats.filter(c => c.access !== 'owner' && !c.indexedCodex)
  const handle = workspace ? '@' + (mode === 'server' ? workspace.user.name : workspace.user.id) : ''
  const quotaBadge = mode === 'server'
    ? serverQuota
      ? serverQuota.bypass5hLimit
        ? 'Без лимита'
        : serverQuota.planTier === 'unlimited'
          ? '5ч ∞'
          : '5ч ' + quotaPercent(serverQuota.fiveHour.usedPercent)
      : null
    : paidApiQuota?.provider === 'paid_api'
      ? 'API ' + dollars(Math.round(paidApiQuota.spentUsd * 100))
      : null

  return (
    <div className="sidebar-inner">
      <div className="traffic-light-spacer" data-tauri-drag-region aria-hidden="true" />
      <div className="brand" data-tauri-drag-region>
        <span className="brand-logo"><CodeXml size={20} strokeWidth={2.4} /></span>
        <strong data-tauri-drag-region>Agent Console</strong>
        <button className="mobile icon-btn" aria-label="Закрыть список чатов" onClick={close}><X size={18} /></button>
      </div>
      <button className="new-chat" onClick={() => { void newChat(); close() }}>
        <Plus size={15} />
        <span>Новый чат</span>
      </button>
      <nav aria-label="Чаты">
        <h2>Мои чаты</h2>
        {chats.filter(c => c.access === 'owner').map(row)}
        {shared.length > 0 && <><h2>Доступные мне</h2>{shared.map(row)}</>}
        {codexIndex !== 'hidden' && <>
          <div className="codex-index-heading">
            <h2>Чаты Codex</h2>
            <button
              className="icon-btn"
              type="button"
              aria-label="Обновить чаты Codex"
              title="Обновить чаты Codex"
              disabled={codexIndex === 'loading'}
              onClick={() => void refreshCodexIndex()}
            >
              <RefreshCw size={14} />
            </button>
          </div>
          {codexIndex === 'loading' && <p className="index-status" role="status">Загружаю чаты…</p>}
          {codexIndex === 'error' && <p className="index-status" role="status">История Codex недоступна.</p>}
          {chats.filter(c => c.indexedCodex).map(row)}
          {codexIndex === 'ready' && !chats.some(c => c.indexedCodex) && <p className="index-status">На сервере нет чатов Codex.</p>}
        </>}
      </nav>
      <div className="side-bottom">
        {mode === 'demo' && (
          <button className="billing-link" onClick={openBilling}>
            <Receipt size={15} />
            <span>Биллинг API</span>
          </button>
        )}
        <div className="side-status-card">
          {handle && (
            <button type="button" className="side-profile-btn" onClick={openSettings} title="Настройки и устройства">
              <span className="side-profile-avatar">{handle.slice(1, 2).toUpperCase()}</span>
              <span className="side-profile-handle">{handle}</span>
              {quotaBadge && <small className="side-quota-pill">{quotaBadge}</small>}
              <Settings2 size={13} className="side-profile-gear" aria-hidden="true" />
            </button>
          )}
          <div className="connection">
            <Radio size={14} />
            <span>{mode === 'demo' ? 'Mock-транспорт' : 'Сервер'}</span>
            <span className={'connection-pill ' + (online ? 'is-online' : 'is-connecting')}>
              <i />{online ? 'Онлайн' : 'Подключение…'}
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}
