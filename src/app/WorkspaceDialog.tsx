import { useEffect, useRef, useState } from 'react'
import { LogOut, Monitor, X } from 'lucide-react'
import type { ShareAccess, Workspace } from '../protocol/types'
import { changeServerAddress, logout, refreshServerDevices, revokeDevice, setConnectionMode, setShare, useChats } from './chatStore'
import { ConnectionMode } from './ConnectionMode'

export const rights = { read: 'Чтение', fork: 'Разветвление (Fork)', write: 'Полный доступ' }
function ShareForm({ w, chatId }: { w: Workspace; chatId: string }) {
  const room = w.chats.find(c => c.id === chatId)!
  const others = w.users.filter(u => u.id !== w.user.id)
  const [user, setUser] = useState(others[0]!.id)
  const existing = room.grants.find(g => g.userId === user)
  const [access, setAccess] = useState<ShareAccess>(existing?.access ?? 'read')
  const [trusted, setTrusted] = useState(!!existing?.trustedDeviceId)
  const [status, setStatus] = useState('')
  function choose(id: string) {
    const g = room.grants.find(g => g.userId === id)
    setUser(id); setAccess(g?.access ?? 'read'); setTrusted(!!g?.trustedDeviceId); setStatus('')
  }
  async function save(remove = false) {
    try {
      await setShare(chatId, user, remove ? null : access, trusted)
      setStatus(remove ? 'Доступ отозван.' : 'Права сохранены.')
      if (remove) { setAccess('read'); setTrusted(false) }
    } catch (e) { setStatus(e instanceof Error ? e.message : 'Не удалось изменить доступ.') }
  }
  return <form className="panel-form" onSubmit={e => { e.preventDefault(); void save() }}>
    <label>Пользователь сервера<select value={user} onChange={e => choose(e.target.value)}>{others.map(u => <option key={u.id} value={u.id}>{u.name} (@{u.id})</option>)}</select></label>
    <label>Права в чате<select value={access} onChange={e => { setAccess(e.target.value as ShareAccess); setTrusted(false); setStatus('') }}>{Object.entries(rights).map(([k,v]) => <option value={k} key={k}>{v}</option>)}</select></label>
    <label className="trust-check"><input type="checkbox" disabled={access !== 'write'} checked={trusted} onChange={e => { setTrusted(e.target.checked); setStatus('') }}/>Доверенный режим для команд на моём ПК</label>
    <p className="form-help">При полном доступе участник сможет запускать демо-команды на этом устройстве без отдельного подтверждения.</p>
    <div className="form-actions"><button className="primary-action">Сохранить доступ</button>{existing && <button type="button" className="alt-action" onClick={() => void save(true)}>Отозвать доступ</button>}</div>
    {status && <p role="status">{status}</p>}
    <div className="members"><h3>Участники с доступом</h3>{room.grants.length ? room.grants.map(g => <button type="button" key={g.userId} onClick={() => choose(g.userId)}><span>{w.users.find(u => u.id === g.userId)?.name}</span><small>{rights[g.access]}{g.trustedDeviceId ? ' · Доверенный ПК' : ''}</small></button>) : <p>Доступ есть только у вас.</p>}</div>
  </form>
}

const deviceActivityFormat = new Intl.DateTimeFormat('ru', { dateStyle: 'medium', timeStyle: 'short' })

function Devices({ w }: { w: Workspace }) {
  const { mode, address, connected } = useChats()
  const [server, setServer] = useState(address)
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [revoking, setRevoking] = useState(false)
  useEffect(() => {
    if (mode !== 'server') return
    let open = true
    void refreshServerDevices().catch(e => {
      if (open) setError(e instanceof Error ? e.message : String(e))
    })
    return () => { open = false }
  }, [mode])
  async function revoke(id: string) {
    setError('')
    setRevoking(true)
    try { await revokeDevice(id); setConfirm('') } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось отозвать устройство.')
    }
    finally { setRevoking(false) }
  }
  const devices = mode === 'server' ? w.devices : w.devices.filter(d => d.ownerId === w.user.id)
  return <>
    <ConnectionMode mode={mode} change={setConnectionMode}/>
    {mode === 'server' && <form className="panel-form server-settings" onSubmit={e => {
      e.preventDefault()
      setError('')
      try { changeServerAddress(server) } catch (e) { setError(e instanceof Error ? e.message : 'Некорректный адрес.') }
    }}>
      <label>Адрес сервера<input type="url" required value={server} onChange={e => setServer(e.target.value)}/></label>
      {server.trim() !== address && <button className="alt-action">Сменить сервер и выйти</button>}
    </form>}
    <p className="form-help">{w.user.name}{mode === 'demo' && ' · @' + w.user.id}</p>
    <div className="device-list">{devices.map(d => {
      const current = d.id === w.deviceId
      const online = mode === 'server' ? connected : d.online
      const lastSeenAt = d.lastSeenAt ?? 0
      return <div key={d.id} className="device-row"><Monitor size={18}/><div><strong>{d.name}</strong>
        {mode === 'demo' || current ? <small>{online ? 'Онлайн' : 'Офлайн'}{current ? ' · Это устройство' : ''}</small> :
          lastSeenAt > 0 && <small>Активность: {deviceActivityFormat.format(lastSeenAt)}</small>}</div>
        {confirm !== d.id ? <button className="text-action" disabled={revoking} onClick={() => setConfirm(d.id)}>Отозвать</button> :
          <div className="device-confirm"><span>{current ? 'Завершить эту сессию?' : 'Отозвать доступ?'}</span>
            <button disabled={revoking} onClick={() => void revoke(d.id)}>Да, отозвать</button><button disabled={revoking} onClick={() => setConfirm('')}>Отмена</button>
          </div>}
      </div>
    })}</div>
    {error && <p role="alert">{error}</p>}<button className="alt-action" onClick={logout}><LogOut size={14}/>Выйти из аккаунта</button></>
}

export function WorkspaceDialog({ kind, w, chatId, close }: { kind: 'share' | 'devices'; w: Workspace; chatId: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => { const el = ref.current!; el.showModal(); return () => el.close() }, [])
  return <dialog className="workspace-dialog" ref={ref} aria-labelledby="panel-heading" onCancel={close} onClick={e => { if (e.target === ref.current) close() }}>
    <section><div className="panel-heading"><h2 id="panel-heading">{kind === 'share' ? 'Поделиться чатом' : 'Мои устройства'}</h2><button className="icon-btn" aria-label="Закрыть" onClick={close}><X size={18}/></button></div>
    {kind === 'share' ? <ShareForm w={w} chatId={chatId}/> : <Devices w={w}/>}</section></dialog>
}
