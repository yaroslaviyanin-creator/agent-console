import { Receipt, CodeXml, Eye, GitFork, MessageSquare, Plus, Radio, RefreshCw, Users, X } from 'lucide-react'
import { newChat, refreshCodexIndex, selectChat, useChats } from './chatStore'
import type { Chat } from './chatStore'

export function Sidebar({ close, openBilling }: { close: () => void; openBilling: () => void }) {
  const { chats, activeId, mode, connected, codexIndex } = useChats()
  function row(c: Chat) {
    const Icon = c.access === 'read' ? Eye : c.access === 'fork' ? GitFork : c.access === 'write' ? Users : MessageSquare
    return <button key={c.id} className={'chat-link ' + (c.id === activeId ? 'selected' : '')} aria-current={c.id === activeId ? 'page' : undefined}
      title={c.title} onClick={() => { selectChat(c.id); close() }}><Icon size={15} aria-label={{ read: 'Чтение', fork: 'Разветвление', write: 'Полный доступ', owner: 'Мой чат' }[c.access]}/><span>{c.title}</span>{c.archived && <small className="archive-label">Архив</small>}</button>
  }
  const shared = chats.filter(c => c.access !== 'owner' && !c.indexedCodex)
  return <div className="sidebar-inner">
    <div className="brand"><span className="brand-logo"><CodeXml size={23} strokeWidth={2.5}/></span><strong>Agent Console</strong><button className="mobile icon-btn" aria-label="Закрыть список чатов" onClick={close}><X size={18}/></button></div>
    <button className="new-chat" onClick={() => { newChat(); close() }}><Plus size={16}/>Новый чат</button>
    <nav aria-label="Чаты"><h2>Мои чаты</h2>{chats.filter(c => c.access === 'owner').map(row)}{shared.length > 0 && <><h2>Доступные мне</h2>{shared.map(row)}</>}
      {codexIndex !== 'hidden' && <>
        <div className="codex-index-heading"><h2>Чаты Codex</h2>
          <button className="icon-btn" type="button" aria-label="Обновить чаты Codex" title="Обновить чаты Codex"
            disabled={codexIndex === 'loading'} onClick={() => void refreshCodexIndex()}><RefreshCw size={14}/></button></div>
        {codexIndex === 'loading' && <p className="index-status" role="status">Загружаю чаты…</p>}
        {codexIndex === 'error' && <p className="index-status" role="status">История Codex недоступна.</p>}
        {chats.filter(c => c.indexedCodex).map(row)}
        {codexIndex === 'ready' && !chats.some(c => c.indexedCodex) && <p className="index-status">На сервере нет чатов Codex.</p>}
      </>}
    </nav>
    <div className="side-bottom">
      {mode === 'demo' && <button className="billing-link" onClick={openBilling}><Receipt size={16}/>Биллинг API</button>}
      <div className="connection"><Radio size={15}/>{mode === 'demo' ? 'Mock-транспорт' : 'Сервер'}<span><i/>{mode === 'demo' || connected ? 'Онлайн' : 'Подключение…'}</span></div>
    </div>
  </div>
}
