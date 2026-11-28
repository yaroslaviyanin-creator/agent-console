import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  Archive, ArchiveRestore, ChevronDown, ChevronRight, CodeXml, Download, Eye, Folder, FolderInput, FolderPlus,
  GitFork, MessageSquare, MoreHorizontal, Palette, Paperclip, Pencil, Pin, PinOff, Plus, Radio, Receipt, Search,
  Settings2, Share2, Shield, Sparkles, Trash2, Users, X,
} from 'lucide-react'
import { ProjectSettingsModal } from './ProjectSettingsModal'
import {
  createChatFolder, deleteChatFolder, deleteChatItem, downloadChatMarkdown, forkChatAt,
  moveChatToFolder, newChat, renameChat, selectChat, toggleArchiveChat, togglePinChat,
  updateChatFolder, useChats, type Chat,
} from './chatStore'
import type { ChatFolder } from '../protocol/types'
import { Quotas, ServerQuotas } from './Quotas'
import { useAccountStore } from './accountStore'
import { UserAvatar } from './UserAvatar'

const planBadges = {
  none: 'FREE',
  pro: 'PRO',
  '2x_pro': '2xPRO',
  unlimited: 'UNLIMITED',
} as const

export const FOLDER_COLORS = ['#6366f1', '#10b981', '#0ea5e9', '#f59e0b', '#f43f5e', '#64748b'] as const
const COLLAPSED_STORAGE_KEY = 'agent-console:collapsed-sections'

function readCollapsedSections(): Record<string, boolean> {
  try {
    if (typeof window === 'undefined' || !window.localStorage) return {}
    const raw = window.localStorage.getItem(COLLAPSED_STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed as Record<string, boolean> : {}
  } catch {
    return {}
  }
}

function saveCollapsedSections(next: Record<string, boolean>) {
  try {
    if (typeof window !== 'undefined' && window.localStorage) {
      window.localStorage.setItem(COLLAPSED_STORAGE_KEY, JSON.stringify(next))
    }
  } catch { /* ignore restricted storage */ }
}

interface MenuState {
  kind: 'chat'
  chatId: string
  x: number
  y: number
}

interface FolderMenuState {
  kind: 'folder'
  folderId: string
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
  openSearch,
}: {
  close: () => void
  openBilling: () => void
  openSettings: () => void
  openAdmin?: (() => void) | undefined
  openShare: (chatId: string) => void
  openSearch?: (() => void) | undefined
}) {
  const { chats, folders, activeId, mode, connected, connectionError, workspace, remote } = useChats()
  const mobileQuotaRef = useRef<HTMLDetailsElement>(null)
  const serverQuota = useAccountStore(s => s.serverQuota)
  const online = mode === 'demo' || (connected && !connectionError)

  const [menu, setMenu] = useState<MenuState | FolderMenuState | null>(null)
  const [folderSubmenuOpen, setFolderSubmenuOpen] = useState(false)
  const [folderColorSubmenuOpen, setFolderColorSubmenuOpen] = useState(false)
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameDraft, setRenameDraft] = useState('')
  const [renamingFolderId, setRenamingFolderId] = useState<string | null>(null)
  const [renameFolderDraft, setRenameFolderDraft] = useState('')
  const [projectModalFolderId, setProjectModalFolderId] = useState<string | null>(null)

  const [creatingFolder, setCreatingFolder] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [newFolderColor, setNewFolderColor] = useState<string>(FOLDER_COLORS[0])
  const [folderCreateError, setFolderCreateError] = useState('')
  const [collapsedSections, setCollapsedSections] = useState<Record<string, boolean>>(() => ({
    archive: true,
    ...readCollapsedSections(),
  }))

  const menuRef = useRef<HTMLDivElement>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const renameFolderInputRef = useRef<HTMLInputElement>(null)
  const newFolderInputRef = useRef<HTMLInputElement>(null)
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

  useEffect(() => {
    if (renamingFolderId) {
      renameFolderInputRef.current?.focus()
      renameFolderInputRef.current?.select()
    }
  }, [renamingFolderId])

  useEffect(() => {
    if (creatingFolder) {
      newFolderInputRef.current?.focus()
    }
  }, [creatingFolder])

  useLayoutEffect(() => {
    if (!menu || !menuRef.current) {
      setMenuPos(null)
      return
    }
    const rect = menuRef.current.getBoundingClientRect()
    setMenuPos(clampMenuPosition(menu.x, menu.y, rect.width || 220, rect.height || 260, window.innerWidth, window.innerHeight))
  }, [menu, confirmDeleteId, folderSubmenuOpen, folderColorSubmenuOpen])

  useEffect(() => {
    if (!menu) return
    const onPointerDown = (e: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenu(null)
        setConfirmDeleteId(null)
        setFolderSubmenuOpen(false)
        setFolderColorSubmenuOpen(false)
      }
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setMenu(null)
        setConfirmDeleteId(null)
        setFolderSubmenuOpen(false)
        setFolderColorSubmenuOpen(false)
      }
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [menu])

  const toggleSection = (key: string) => {
    setCollapsedSections(prev => {
      const next = { ...prev, [key]: !prev[key] }
      saveCollapsedSections(next)
      return next
    })
  }

  const clearLongPress = () => {
    if (longPressTimerRef.current) {
      clearTimeout(longPressTimerRef.current)
      longPressTimerRef.current = null
    }
    longPressOriginRef.current = null
  }

  const openMenuAt = (chatId: string, x: number, y: number) => {
    setConfirmDeleteId(null)
    setFolderSubmenuOpen(false)
    setFolderColorSubmenuOpen(false)
    setMenu({ kind: 'chat', chatId, x, y })
  }

  const openFolderMenuAt = (folderId: string, x: number, y: number) => {
    setConfirmDeleteId(null)
    setFolderSubmenuOpen(false)
    setFolderColorSubmenuOpen(false)
    setMenu({ kind: 'folder', folderId, x, y })
  }

  const commitRename = (chatId: string) => {
    const next = renameDraft.trim()
    setRenamingId(null)
    if (next) void renameChat(chatId, next)
  }

  const commitFolderRename = (folderId: string) => {
    const next = renameFolderDraft.trim()
    setRenamingFolderId(null)
    if (next) void updateChatFolder(folderId, { name: next })
  }

  const submitCreateFolder = async () => {
    const trimmed = newFolderName.trim()
    if (!trimmed) return
    setFolderCreateError('')
    try {
      await createChatFolder(trimmed, newFolderColor)
      setCreatingFolder(false)
      setNewFolderName('')
    } catch (err) {
      setFolderCreateError(err instanceof Error ? err.message : 'Не удалось создать папку.')
    }
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
        className={'chat-row-item' + (c.id === activeId ? ' selected' : '') + (menu?.kind === 'chat' && menu.chatId === c.id ? ' menu-open' : '')}
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
          {c.draft.trim().length > 0 && <small className="draft-label">Черновик</small>}
          {c.pinned && <Pin size={12} className="chat-pin-badge" aria-label="Закреплён" />}
          {(c.archived || c.archivedAt != null) && <small className="archive-label">Архив</small>}
        </button>
        {showMenuTrigger && (
          <button
            type="button"
            className="chat-more-btn"
            aria-label={`Действия с чатом ${c.title}`}
            aria-haspopup="menu"
            aria-expanded={menu?.kind === 'chat' && menu.chatId === c.id}
            onClick={e => {
              e.stopPropagation()
              const rect = e.currentTarget.getBoundingClientRect()
              if (menu?.kind === 'chat' && menu.chatId === c.id) {
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

  const ownerChats = chats.filter(c => c.access === 'owner')
  const archivedOwnerChats = ownerChats.filter(c => c.archivedAt != null)
  const activeOwnerChats = ownerChats.filter(c => c.archivedAt == null)
  const folderIdSet = new Set(folders.map(f => f.id))
  const unfiledOwnerChats = activeOwnerChats.filter(c => !c.folderId || !folderIdSet.has(c.folderId))
  const shared = chats.filter(c => c.access !== 'owner' && !c.indexedCodex)

  const menuChat = menu?.kind === 'chat' ? chats.find(c => c.id === menu.chatId) : undefined
  const menuFolder: ChatFolder | undefined = menu?.kind === 'folder' ? folders.find(f => f.id === menu.folderId) : undefined

  const handle = workspace ? '@' + (mode === 'server' ? workspace.user.name : workspace.user.id) : ''
  const profileDisplayName = workspace ? (workspace.user.displayName?.trim() || workspace.user.name) : ''
  const planBadge = mode === 'server'
    ? serverQuota
      ? { label: planBadges[serverQuota.planTier], tone: serverQuota.planTier }
      : workspace?.user.role === 'admin'
        ? { label: 'ADMIN', tone: 'unlimited' }
        : null
    : { label: 'DEMO', tone: 'demo' }

  const contextMenuNode = menu && (menuChat || menuFolder) && typeof document !== 'undefined'
    ? createPortal(
      <div
        ref={menuRef}
        className="chat-context-menu"
        role="menu"
        aria-label={menuChat ? `Меню чата ${menuChat.title}` : `Меню папки ${menuFolder?.name}`}
        style={menuPos ? { left: `${menuPos.left}px`, top: `${menuPos.top}px` } : { left: `${menu.x}px`, top: `${menu.y}px` }}
      >
        {menuChat && (
          <>
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
                <button
                  type="button"
                  role="menuitem"
                  aria-expanded={folderSubmenuOpen}
                  className="chat-menu-item"
                  onClick={() => setFolderSubmenuOpen(v => !v)}
                >
                  <FolderInput size={14} />
                  <span>Переместить в папку ▸</span>
                </button>
                {folderSubmenuOpen && (
                  <div className="chat-menu-submenu" role="group" aria-label="Выбор папки">
                    <button
                      type="button"
                      role="menuitem"
                      className={`chat-menu-item is-sub${!menuChat.folderId ? ' is-current' : ''}`}
                      onClick={() => {
                        setMenu(null)
                        void moveChatToFolder(menuChat.id, null)
                      }}
                    >
                      <span>Без папки</span>
                    </button>
                    {folders.map(f => (
                      <button
                        key={f.id}
                        type="button"
                        role="menuitem"
                        className={`chat-menu-item is-sub${menuChat.folderId === f.id ? ' is-current' : ''}`}
                        onClick={() => {
                          setMenu(null)
                          void moveChatToFolder(menuChat.id, f.id)
                        }}
                      >
                        <span className="folder-dot" style={{ backgroundColor: f.color }} />
                        <span>{f.name}</span>
                      </button>
                    ))}
                  </div>
                )}
                <button
                  type="button"
                  role="menuitem"
                  className="chat-menu-item"
                  onClick={() => {
                    setMenu(null)
                    void toggleArchiveChat(menuChat.id)
                  }}
                >
                  {menuChat.archivedAt != null ? <ArchiveRestore size={14} /> : <Archive size={14} />}
                  <span>{menuChat.archivedAt != null ? 'Вернуть из архива' : 'Архивировать'}</span>
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
              <span>Экспорт в Markdown (.md)</span>
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
          </>
        )}
        {menuFolder && (
          <>
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item"
              onClick={() => {
                setMenu(null)
                setProjectModalFolderId(menuFolder.id)
              }}
            >
              <Settings2 size={14} />
              <span>Настройки проекта</span>
            </button>
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item"
              onClick={() => {
                setMenu(null)
                setRenameFolderDraft(menuFolder.name)
                setRenamingFolderId(menuFolder.id)
              }}
            >
              <Pencil size={14} />
              <span>Переименовать</span>
            </button>
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item"
              aria-expanded={folderColorSubmenuOpen}
              onClick={() => setFolderColorSubmenuOpen(v => !v)}
            >
              <Palette size={14} />
              <span>Цвет ▸</span>
            </button>
            {folderColorSubmenuOpen && (
              <div className="folder-color-row" role="group" aria-label="Цвет папки">
                {FOLDER_COLORS.map(c => (
                  <button
                    key={c}
                    type="button"
                    aria-label={`Выбрать цвет ${c}`}
                    className={`folder-color-swatch${menuFolder.color.toLowerCase() === c ? ' is-active' : ''}`}
                    style={{ backgroundColor: c }}
                    onClick={() => {
                      setMenu(null)
                      void updateChatFolder(menuFolder.id, { color: c })
                    }}
                  />
                ))}
              </div>
            )}
            <button
              type="button"
              role="menuitem"
              className="chat-menu-item is-danger"
              onClick={() => {
                setMenu(null)
                void deleteChatFolder(menuFolder.id)
              }}
            >
              <Trash2 size={14} />
              <span>Удалить папку</span>
            </button>
          </>
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
      <div className="side-actions-row">
        <button className="new-chat" onClick={() => { void newChat(); close() }}>
          <Plus size={15} />
          <span>Новый чат</span>
        </button>
        {openSearch && (
          <button
            type="button"
            className="side-search-btn"
            aria-label="Поиск по чатам и сообщениям (Cmd+K)"
            title="Поиск по чатам (⌘K / Ctrl+K)"
            onClick={openSearch}
          >
            <Search size={14} aria-hidden="true" />
            <kbd className="side-search-kbd">⌘K</kbd>
          </button>
        )}
      </div>
      <nav aria-label="Чаты">
        <div className="side-section-head">
          <h2>Мои чаты</h2>
          <button
            type="button"
            className="side-folder-add-btn"
            title="Новая папка"
            aria-label="Новая папка"
            onClick={() => setCreatingFolder(v => !v)}
          >
            <FolderPlus size={13} aria-hidden="true" />
            <span>Папка</span>
          </button>
        </div>

        {creatingFolder && (
            <form
            className="folder-create-box"
            onSubmit={e => {
              e.preventDefault()
              void submitCreateFolder()
            }}
          >
            <input
              ref={newFolderInputRef}
              type="text"
              className="folder-create-input"
              placeholder="Название папки…"
              maxLength={60}
              value={newFolderName}
              onChange={e => {
                setNewFolderName(e.target.value)
                if (folderCreateError) setFolderCreateError('')
              }}
              onKeyDown={e => {
                if (e.key === 'Escape') {
                  setCreatingFolder(false)
                  setNewFolderName('')
                  setFolderCreateError('')
                }
              }}
            />
            {folderCreateError && <p className="auth-error" role="alert">{folderCreateError}</p>}
            <div className="folder-color-row">
              {FOLDER_COLORS.map(c => (
                <button
                  key={c}
                  type="button"
                  aria-label={`Цвет ${c}`}
                  className={`folder-color-swatch${newFolderColor === c ? ' is-active' : ''}`}
                  style={{ backgroundColor: c }}
                  onClick={() => setNewFolderColor(c)}
                />
              ))}
            </div>
            <div className="folder-create-actions">
              <button type="submit" className="primary-action" disabled={!newFolderName.trim()}>Создать</button>
              <button type="button" className="alt-action" onClick={() => { setCreatingFolder(false); setNewFolderName('') }}>Отмена</button>
            </div>
          </form>
        )}

        {folders.map(folder => {
          const folderChats = activeOwnerChats.filter(c => c.folderId === folder.id)
          const isCollapsed = Boolean(collapsedSections[`folder:${folder.id}`])
          const isRenamingFolder = renamingFolderId === folder.id
          return (
            <div key={folder.id} className="folder-group">
              <div
                className="folder-group-head"
                onContextMenu={e => {
                  e.preventDefault()
                  openFolderMenuAt(folder.id, e.clientX, e.clientY)
                }}
              >
                {isRenamingFolder ? (
                  <input
                    ref={renameFolderInputRef}
                    type="text"
                    className="chat-rename-input"
                    aria-label="Новое название папки"
                    value={renameFolderDraft}
                    maxLength={60}
                    onChange={e => setRenameFolderDraft(e.target.value)}
                    onBlur={() => commitFolderRename(folder.id)}
                    onKeyDown={e => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        commitFolderRename(folder.id)
                      } else if (e.key === 'Escape') {
                        e.preventDefault()
                        setRenamingFolderId(null)
                      }
                    }}
                  />
                ) : (
                  <>
                    <button
                      type="button"
                      className="folder-toggle-btn"
                      aria-expanded={!isCollapsed}
                      onClick={() => toggleSection(`folder:${folder.id}`)}
                    >
                      {isCollapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
                      <Folder size={14} style={{ color: folder.color }} aria-hidden="true" />
                      <span className="folder-name">{folder.name}</span>
                      {(folder.systemPrompt?.trim() || (folder.attachmentCount ?? 0) > 0) && (
                        <span className="folder-project-badge" title="Проект: заданы инструкции или файлы">
                          {folder.systemPrompt?.trim() && <Sparkles size={10} aria-hidden="true" />}
                          {(folder.attachmentCount ?? 0) > 0 && (
                            <span className="folder-file-count">
                              <Paperclip size={10} aria-hidden="true" />
                              {folder.attachmentCount}
                            </span>
                          )}
                        </span>
                      )}
                      <small className="folder-count">{folderChats.length}</small>
                    </button>
                    <button
                      type="button"
                      className="chat-more-btn folder-more-btn"
                      aria-label={`Действия с папкой ${folder.name}`}
                      onClick={e => {
                        e.stopPropagation()
                        const rect = e.currentTarget.getBoundingClientRect()
                        openFolderMenuAt(folder.id, rect.right, rect.bottom + 4)
                      }}
                    >
                      <MoreHorizontal size={13} />
                    </button>
                  </>
                )}
              </div>
              {!isCollapsed && (
                <div className="folder-group-items">
                  {folderChats.length > 0
                    ? folderChats.map(row)
                    : <div className="folder-empty-hint">Пусто</div>}
                </div>
              )}
            </div>
          )
        })}

        {unfiledOwnerChats.map(row)}

        {shared.length > 0 && <><h2>Доступные мне</h2>{shared.map(row)}</>}

        {archivedOwnerChats.length > 0 && (
          <div className="archive-section">
            <button
              type="button"
              className="archive-toggle-btn"
              aria-expanded={!collapsedSections.archive}
              onClick={() => toggleSection('archive')}
            >
              {collapsedSections.archive ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
              <Archive size={13} aria-hidden="true" />
              <span>Архив ({archivedOwnerChats.length})</span>
            </button>
            {!collapsedSections.archive && (
              <div className="archive-items">
                {archivedOwnerChats.map(row)}
              </div>
            )}
          </div>
        )}
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
      {projectModalFolderId && (() => {
        const targetFolder = folders.find(f => f.id === projectModalFolderId)
        if (!targetFolder) return null
        return createPortal(
          <ProjectSettingsModal folder={targetFolder} onClose={() => setProjectModalFolderId(null)} />,
          document.body,
        )
      })()}
    </div>
  )
}
