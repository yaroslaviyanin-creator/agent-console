import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  CodeXml, Download, Eye, GitFork, MessageSquare, MoreHorizontal, Pencil, Pin, PinOff, Plus, Radio, Receipt, Settings2, Share2, Shield, Trash2, Users, X,
} from 'lucide-react'
import {
  deleteChatItem, downloadChatMarkdown, forkChatAt, newChat, renameChat, selectChat, togglePinChat, useChats,
  type Chat,
} from './chatStore'
import { Quotas, ServerQuotas } from './Quotas'
import { useAccountStore } from './accountStore'
import { UserAvatar } from './UserAvatar'

const planBadges = {
  none: 'FREE',
  pro: 'PRO',
  '2x_pro': '2xPRO',
  unlimited: 'UNLIMITED',
} as const

interface MenuState {
  chatId: string
  x: number
  y: number
}

export function clampMenuPosition(x: number, y: number, width: number, height: number, vw: number, vh: number) {
  const pad = 8
  return {
    left: Math.max(pad, Math.min(x, Math.max(pad, vw - width - pad))),
    top: Math.max(pad, Math.min(y, Math.max(pad, vh - height - pad))),
  }
}

export function Sidebar({
  close,
  openBilling,
  openSettings,
  openAdmin,
  openShare,
}: {
  close: () => void
  openBilling: () => void
  openSettings: () => void
  openAdmin?: (() => void) | undefined
  openShare: (chatId: string) => void
}) {
  const { chats, activeId, mode, connected, connectionError, workspace, remote } = useChats()
  const mobileQuotaRef = useRef<HTMLDetailsElement>(null)
  const serverQuota = useAccountStore(s => s.serverQuota)
  const online = mode === 'demo' || (connected && !connectionError)
  const [menu, setMenu] = useState<MenuState | null>(null)
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameDraft, setRenameDraft] = useState('')
  const menuRef = useRef<HTMLDivElement>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const longPressTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const longPressOriginRef = useRef<{ x: number; y: number } | null>(null)
  const suppressNextClickRef = useRef(false)
  const [menuPos, setMenuPos] = useState<{ left: number; top: number } | null>(null)

  useEffect(() => {
    if (renamingId) {
      renameInputRef.current?.focus()
      renameInputRef.current?.select()
    }
  }, [renamingId])

  useLayoutEffect(() => {
    if (!menu || !menuRef.current) {
      setMenuPos(null)
      return
    }
    const rect = menuRef.current.getBoundingClientRect()
    setMenuPos(clampMenuPosition(menu.x, menu.y, rect.width || 210, rect.height || 240, window.innerWidth, window.innerHeight))
  }, [menu, confirmDeleteId])

  useEffect(() => {
    if (!menu) return
    const onPointerDown = (e: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenu(null)
        setConfirmDeleteId(null)
      }
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setMenu(null)
        setConfirmDeleteId(null)
      }
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [menu])

  const clearLongPress = () => {
    if (longPressTimerRef.current) {
      clearTimeout(longPressTimerRef.current)
      longPressTimerRef.current = null
    }
    longPressOriginRef.current = null
  }

  const openMenuAt = (chatId: string, x: number, y: number) => {
    setConfirmDeleteId(null)
    setMenu({ chatId, x, y })
  }

  const commitRename = (chatId: string) => {
    const next = renameDraft.trim()
    setRenamingId(null)
    if (next) void renameChat(chatId, next)
  }

  function row(c: Chat) {
    const Icon = c.access === 'read' ? Eye : c.access === 'fork' ? GitFork : c.access === 'write' ? Users : MessageSquare
    const isRenaming = renamingId === c.id
    const showMenuTrigger = !c.indexedCodex

    if (isRenaming) {
      return (
        <div key={c.id} className={'chat-row-item is-renaming ' + (c.id === activeId ? 'selected' : '')}>
          <Icon size={15} />
          <input
            ref={renameInputRef}
            type="text"
            className="chat-rename-input"
            aria-label="Новое название чата"
            value={renameDraft}
            maxLength={200}
            onChange={e => setRenameDraft(e.target.value)}
            onBlur={() => commitRename(c.id)}
            onKeyDown={e => {
              if (e.key === 'Enter') {
                e.preventDefault()
                commitRename(c.id)
              } else if (e.key === 'Escape') {
                e.preventDefault()
                setRenamingId(null)
              }
            }}
          />
        </div>
      )
    }

    return (
      <div
        key={c.id}
        className={'chat-row-item' + (c.id === activeId ? ' selected' : '') + (menu?.chatId === c.id ? ' menu-open' : '')}
        onContextMenu={e => {
          if (!showMenuTrigger) return
          e.preventDefault()
          openMenuAt(c.id, e.clientX, e.clientY)
        }}
      >
        <button
          className={'chat-link ' + (c.id === activeId ? 'selected' : '')}
          aria-current={c.id === activeId ? 'page' : undefined}
          title={c.title}
          onPointerDown={e => {
            if (!showMenuTrigger || e.pointerType === 'mouse') return
            clearLongPress()
            longPressOriginRef.current = { x: e.clientX, y: e.clientY }
            const cx = e.clientX
            const cy = e.clientY
            longPressTimerRef.current = setTimeout(() => {
              suppressNextClickRef.current = true
              openMenuAt(c.id, cx, cy)
              clearLongPress()
            }, 480)
          }}
          onPointerMove={e => {
            if (!longPressOriginRef.current) return
            if (Math.hypot(e.clientX - longPressOriginRef.current.x, e.clientY - longPressOriginRef.current.y) > 8) {
              clearLongPress()
            }
          }}
          onPointerUp={clearLongPress}
          onPointerCancel={clearLongPress}
          onClick={e => {
            if (suppressNextClickRef.current) {
              suppressNextClickRef.current = false
              e.preventDefault()
              return
            }
            selectChat(c.id)
            close()
          }}
        >
          <Icon size={15} aria-label={{ read: 'Чтение', fork: 'Разветвление', write: 'Полный доступ', owner: 'Мой чат' }[c.access]} />
          <span className="chat-link-title">{c.title}</span>
          {c.pinned && <Pin size={12} className="chat-pin-badge" aria-label="Закреплён" />}
          {c.archived && <small className="archive-label">Архив</small>}
        </button>
        {showMenuTrigger && (
          <button
            type="button"
            className="chat-more-btn"
            aria-label={`Действия с чатом ${c.title}`}
            aria-haspopup="menu"
            aria-expanded={menu?.chatId === c.id}
            onClick={e => {
              e.stopPropagation()
              const rect = e.currentTarget.getBoundingClientRect()
              if (menu?.chatId === c.id) {
                setMenu(null)
              } else {
                openMenuAt(c.id, rect.right, rect.bottom + 4)
              }
            }}
          >
            <MoreHorizontal size={14} />
          </button>
        )}
      </div>
    )
  }

  const menuChat = menu ? chats.find(c => c.id === menu.chatId) : undefined
  const shared = chats.filter(c => c.access !== 'owner' && !c.indexedCodex)
  const handle = workspace ? '@' + (mode === 'server' ? workspace.user.name : workspace.user.id) : ''
  const profileDisplayName = workspace ? (workspace.user.displayName?.trim() || workspace.user.name) : ''
  const planBadge = mode === 'server'
    ? serverQuota
      ? { label: planBadges[serverQuota.planTier], tone: serverQuota.planTier }
      : workspace?.user.role === 'admin'
        ? { label: 'ADMIN', tone: 'unlimited' }
        : null
    : { label: 'DEMO', tone: 'demo' }

  const contextMenuNode = menu && menuChat && typeof document !== 'undefined'
    ? createPortal(
      <div
        ref={menuRef}
        className="chat-context-menu"
        role="menu"
        aria-label={`Меню чата ${menuChat.title}`}
        style={menuPos ? { left: `${menuPos.left}px`, top: `${menuPos.top}px` } : { left: `${menu.x}px`, top: `${menu.y}px` }}
      >
        {menuChat.access === 'owner' && !menuChat.indexedCodex && (
          <>
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item"
              onClick={() => {
                setMenu(null)
                setRenameDraft(menuChat.title)
                setRenamingId(menuChat.id)
              }}
            >
              <Pencil size={14} />
              <span>Переименовать</span>
            </button>
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item"
              onClick={() => {
                setMenu(null)
                void togglePinChat(menuChat.id)
              }}
            >
              {menuChat.pinned ? <PinOff size={14} /> : <Pin size={14} />}
              <span>{menuChat.pinned ? 'Открепить' : 'Закрепить'}</span>
            </button>
          </>
        )}
        {menuChat.access !== 'read' && !menuChat.indexedCodex && (
          <button
            type="button"
            role="menuitem"
            className="chat-menu-item"
            onClick={() => {
              setMenu(null)
              void forkChatAt(menuChat.id)
              close()
            }}
          >
            <GitFork size={14} />
            <span>Создать ветку чата</span>
          </button>
        )}
        {menuChat.access === 'owner' && !menuChat.indexedCodex && (
          <button
            type="button"
            role="menuitem"
            className="chat-menu-item"
            onClick={() => {
              setMenu(null)
              close()
              openShare(menuChat.id)
            }}
          >
            <Share2 size={14} />
            <span>Поделиться</span>
          </button>
        )}
        <button
          type="button"
          role="menuitem"
          className="chat-menu-item"
          onClick={() => {
            setMenu(null)
            downloadChatMarkdown(menuChat)
          }}
        >
          <Download size={14} />
          <span>Экспорт в Markdown</span>
        </button>
        {menuChat.access === 'owner' && !menuChat.indexedCodex && (
          confirmDeleteId === menuChat.id ? (
            <div className="chat-menu-confirm">
              <span>Удалить чат безвозвратно?</span>
              <div className="chat-menu-confirm-btns">
                <button
                  type="button"
                  className="is-danger"
                  onClick={() => {
                    setMenu(null)
                    setConfirmDeleteId(null)
                    void deleteChatItem(menuChat.id)
                  }}
                >
                  Удалить
                </button>
                <button type="button" onClick={() => setConfirmDeleteId(null)}>Отмена</button>
              </div>
            </div>
          ) : (
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item is-danger"
              onClick={() => setConfirmDeleteId(menuChat.id)}
            >
              <Trash2 size={14} />
              <span>Удалить</span>
            </button>
          )
        )}
      </div>,
      document.body,
    )
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
      </nav>
      <div className="side-bottom">
        {workspace && (
          <div className="side-mobile-meta">
            <div className="side-mobile-quotas">
              {remote
                ? <ServerQuotas menuRef={mobileQuotaRef} onOpenChange={() => {}} />
                : <Quotas userId={workspace.user.id} menuRef={mobileQuotaRef} onOpenChange={() => {}} />}
            </div>
            {openAdmin && (
              <button type="button" className="alt-action side-admin-btn" onClick={openAdmin}>
                <Shield size={14} aria-hidden="true" />
                <span>Админка</span>
              </button>
            )}
          </div>
        )}
        {mode === 'demo' && (
          <button className="billing-link" onClick={openBilling}>
            <Receipt size={15} />
            <span>Биллинг API</span>
          </button>
        )}
        <div className="side-status-card">
          {handle && (
            <button type="button" className="side-profile-btn" onClick={openSettings} title="Настройки и устройства">
              <UserAvatar name={profileDisplayName} avatarData={workspace?.user.avatarData} className="side-profile-avatar" />
              <span className="side-profile-handle">{handle}</span>
              {planBadge && <small className="side-plan-badge" data-plan={planBadge.tone}>{planBadge.label}</small>}
              <Settings2 size={13} className="side-profile-gear" aria-hidden="true" />
            </button>
          )}
          <div className="connection">
            <Radio size={14} />
            <span>{mode === 'demo' ? 'Mock-транспорт' : 'Сервер'}</span>
            <span className={'connection-pill ' + (online ? 'is-online' : 'is-connecting')}>
              <i />{online ? 'Онлайн' : 'Переподключение…'}
            </span>
          </div>
        </div>
      </div>
      {contextMenuNode}
    </div>
  )
}
