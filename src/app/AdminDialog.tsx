import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { X } from 'lucide-react'
import type { AdminChat, AdminUser, PlanTier, ServerAdminAdapter, ServerMessage, UserRole } from '../protocol/types'
import { RichText } from './Markdown'

const roleLabel = { pending: 'Ожидает', user: 'Пользователь', admin: 'Администратор' }
import { dollars, limitDollars, quotaPercent } from './Quotas'

const planLabel: Record<PlanTier, string> = { none: 'Нет', pro: 'Pro', '2x_pro': '2x Pro', unlimited: 'Безлимит' }
const plans = Object.keys(planLabel) as PlanTier[]
const roles = Object.keys(roleLabel) as UserRole[]
const emptyForm = { username: '', password: '', role: 'user' as UserRole, planTier: 'none' as PlanTier }
const filters = { all: 'Все', pending: 'Ожидают', active: 'Активные', blocked: 'Заблокированные' }
type Filter = keyof typeof filters
const thirtyDays = 30 * 24 * 60 * 60 * 1000
const date = new Intl.DateTimeFormat('ru', { dateStyle: 'medium' })
const dateTime = new Intl.DateTimeFormat('ru', { dateStyle: 'short', timeStyle: 'short' })
const statusOf = (user: AdminUser) => !user.isActive ? 'blocked' : user.role === 'pending' ? 'pending' : 'active'
const failure = (error: unknown) => error instanceof Error ? error.message : 'Операция не выполнена.'
const generatePassword = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')

type UserAction = { user: AdminUser; type: 'password' | 'block' | 'revoke' }

export function AdminDialog({ admin, userId, close }: { admin: ServerAdminAdapter; userId: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const [users, setUsers] = useState<AdminUser[]>([])
  const [form, setForm] = useState(emptyForm)
  const [filter, setFilter] = useState<Filter>('all')
  const [search, setSearch] = useState('')
  const [historyUser, setHistoryUser] = useState<AdminUser | null>(null)
  const [action, setAction] = useState<UserAction | null>(null)
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    const dialog = ref.current!
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void admin.list(controller.signal).then(result => {
      if (!controller.signal.aborted) setUsers(result)
    }).catch(reason => {
      if (!controller.signal.aborted) setError(failure(reason))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [admin, reload])

  async function mutate(run: () => Promise<unknown>, message: string, after?: () => void) {
    setBusy(true)
    setError('')
    setStatus('')
    try {
      await run()
      after?.()
      try {
        setUsers(await admin.list())
      } catch {
        setError(`${message} Не удалось обновить список пользователей.`)
        return
      }
      setStatus(message)
    } catch (reason) {
      setError(failure(reason))
    } finally {
      setBusy(false)
    }
  }

  function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    void mutate(() => admin.create({ ...form, username: form.username.trim() }), 'Пользователь создан.', () => setForm(emptyForm))
  }

  function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!action) return
    const { type, user } = action
    const run = type === 'revoke' ? () => admin.revokeDevices(user.id)
      : () => admin.update(user.id, type === 'password' ? { newPassword: password } : { isActive: false })
    const message = type === 'revoke' ? 'Сессии завершены.' : type === 'password' ? 'Пароль изменён. Сессии завершены.' : 'Пользователь заблокирован.'
    void mutate(run, message, () => { setAction(null); setPassword('') })
  }

  const visibleUsers = users.filter(user => (filter === 'all' || statusOf(user) === filter) &&
    user.username.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))

  return <dialog ref={ref} className="workspace-dialog admin-dialog" aria-labelledby="admin-heading" onCancel={close} onClick={event => { if (event.target === ref.current) close() }}>
    <section>
      <header className="panel-heading"><h2 id="admin-heading">Админка</h2><button type="button" className="icon-btn" onClick={close} aria-label="Закрыть"><X size={18}/></button></header>
      {historyUser ? <AdminUserHistory admin={admin} user={historyUser} back={() => {
        setHistoryUser(null)
        ref.current?.scrollTo(0, 0)
      }}/> : <>
        <dl className="admin-summary">
          <div><dt>Всего</dt><dd>{users.length}</dd></div>
          <div><dt>Ожидают</dt><dd>{users.filter(user => statusOf(user) === 'pending').length}</dd></div>
          <div><dt>Активных</dt><dd>{users.filter(user => statusOf(user) === 'active').length}</dd></div>
          <div><dt>Чатов</dt><dd>{users.reduce((total, user) => total + user.chatsCount, 0)}</dd></div>
        </dl>
        {error && <p className="notice" role="alert">{error}</p>}
        {status && <p className="form-help" role="status">{status}</p>}
        <details className="admin-create">
          <summary>+ Создать вручную</summary>
          <form className="panel-form" onSubmit={create}>
            <fieldset disabled={busy || loading}>
              <label>Логин<input required minLength={1} maxLength={64} autoCapitalize="none" autoComplete="off" value={form.username} onChange={event => setForm({ ...form, username: event.target.value })}/></label>
              <label>Роль<select value={form.role} onChange={event => setForm({ ...form, role: event.target.value as UserRole })}>{roles.map(role => <option key={role} value={role}>{roleLabel[role]}</option>)}</select></label>
              <label>Тариф<select value={form.planTier} onChange={event => setForm({ ...form, planTier: event.target.value as PlanTier })}>
                {plans.map(plan => <option key={plan} value={plan}>{planLabel[plan]}</option>)}
              </select></label>
              <div className="admin-password"><label>Пароль<input required maxLength={72} autoComplete="new-password" spellCheck={false} value={form.password} onChange={event => setForm({ ...form, password: event.target.value })}/></label>
                <button type="button" className="alt-action" onClick={() => setForm({ ...form, password: generatePassword() })}>Сгенерировать</button></div>
              <button className="alt-action" type="submit">Создать</button>
            </fieldset>
          </form>
        </details>
        <div className="panel-heading admin-list-heading">
          <h3>Пользователи</h3><button type="button" className="alt-action" disabled={busy || loading} onClick={() => { setError(''); setLoading(true); setReload(value => value + 1) }}>Обновить</button>
        </div>
        <div className="admin-filters" role="group" aria-label="Статус пользователей">
          {(Object.keys(filters) as Filter[]).map(key => <button key={key} type="button" className="alt-action" aria-pressed={key === filter} onClick={() => setFilter(key)}>{filters[key]}</button>)}
        </div>
        <div className="panel-form admin-search"><input type="search" aria-label="Поиск по логину" placeholder="Поиск по логину" value={search} onChange={event => setSearch(event.target.value)}/></div>
        {loading && <p className="form-help" role="status">Загрузка…</p>}
        {!loading && !visibleUsers.length && !error && <p className="form-help">Пользователи не найдены.</p>}
        <ul className="admin-users">
          {visibleUsers.map(user => <li key={user.id} className="admin-user">
            <div className="admin-user-heading">
              <strong>{user.username}{user.id === userId && ' (вы)'}</strong>
              <div className="admin-badges">
                <span>{roleLabel[user.role]}</span>
                <span data-active={user.role === 'pending' && user.isActive ? undefined : user.isActive}>{!user.isActive ? 'Заблокирован' : user.role === 'pending' ? 'Ждёт доступа' : 'Активен'}</span>
                <span>{user.planTier === 'none' ? 'Без тарифа' : planLabel[user.planTier]}</span>
                {user.bypass5hLimit && <span>Без 5ч</span>}
              </div>
            </div>
            <div className="admin-usage">
              <Usage label="5ч" usage={user.usage5h} limit={user.limit5h} unlimited={user.planTier === 'unlimited'} bypass={user.bypass5hLimit}/>
              <Usage label="7д" usage={user.usage7d} limit={user.limit7d} unlimited={user.planTier === 'unlimited'}/>
            </div>
            <p className="form-help">Устройств: {user.devicesCount} · {user.subscriptionUntil ? 'Подписка до ' + date.format(user.subscriptionUntil) : user.planTier === 'none' ? 'Подписка не подключена' : 'Бессрочная подписка'}</p>
            <fieldset className="admin-actions" disabled={busy || loading}>
              <label className="admin-role">Тариф<select aria-label={'Тариф пользователя ' + user.username} value={user.planTier} onChange={event => {
                const planTier = event.target.value as PlanTier
                void mutate(() => admin.update(user.id, { planTier }), 'Тариф обновлён.')
              }}>{plans.map(plan => <option key={plan} value={plan}>{planLabel[plan]}</option>)}</select></label>
              <label className="admin-role"><input type="checkbox" checked={user.bypass5hLimit} onChange={event => {
                const bypass5hLimit = event.target.checked
                void mutate(() => admin.update(user.id, { bypass5hLimit }), 'Лимит обновлён.')
              }}/>Отключить 5ч лимит</label>
              <button type="button" className="alt-action" onClick={() => void mutate(() => admin.update(user.id, {
                subscriptionUntil: Math.max(Date.now(), user.subscriptionUntil) + thirtyDays,
              }), 'Срок подписки обновлён.')}>+30 дней</button>
              <button type="button" className="alt-action" onClick={() => {
                setHistoryUser(user)
                ref.current?.scrollTo(0, 0)
              }}>Чаты ({user.chatsCount})</button>
              {user.role === 'pending' && <button type="button" className="primary-action" onClick={() => void mutate(() => admin.update(user.id, {
                role: 'user', isActive: true, planTier: 'pro', bypass5hLimit: false, subscriptionUntil: Date.now() + thirtyDays,
              }), 'Доступ выдан.')}>Одобрить (Pro, 30 дн)</button>}
            </fieldset>
            {user.id !== userId && <fieldset className="admin-actions" disabled={busy || loading}>
              <label className="admin-role">Роль<select aria-label={'Роль пользователя ' + user.username} value={user.role} onChange={event => {
                const role = event.target.value as UserRole
                void mutate(() => admin.update(user.id, { role }), 'Роль обновлена.')
              }}>{roles.map(role => <option key={role} value={role}>{roleLabel[role]}</option>)}</select></label>
              <button type="button" className="alt-action" onClick={() => { setPassword(''); setAction({ user, type: 'password' }) }}>Сбросить пароль</button>
              <button type="button" className="alt-action" onClick={() => {
                if (user.isActive) setAction({ user, type: 'block' })
                else void mutate(() => admin.update(user.id, { isActive: true }), 'Пользователь разблокирован.')
              }}>{user.isActive ? 'Заблокировать' : 'Разблокировать'}</button>
              <button type="button" className="alt-action" onClick={() => setAction({ user, type: 'revoke' })}>Выгнать со всех устройств</button>
            </fieldset>}
            {action?.user.id === user.id && <form className="panel-form admin-confirm" onSubmit={confirm}>
              <fieldset disabled={busy}>
                {action.type === 'password' ? <div className="admin-password">
                  <label>Новый пароль<input required maxLength={72} autoComplete="new-password" spellCheck={false} value={password} onChange={event => setPassword(event.target.value)}/></label>
                  <button type="button" className="alt-action" onClick={() => setPassword(generatePassword())}>Сгенерировать</button>
                </div> : <p>{action.type === 'block' ? 'Заблокировать аккаунт и закрыть соединения?' : 'Завершить все сессии пользователя?'}</p>}
                <div className="admin-confirm-actions">
                  <button className="alt-action" type="submit">{action.type === 'password' ? 'Сохранить пароль' : 'Подтвердить'}</button>
                  <button className="alt-action" type="button" onClick={() => { setAction(null); setPassword('') }}>Отмена</button>
                </div>
              </fieldset>
            </form>}
          </li>)}
        </ul>
      </>}
    </section>
  </dialog>
}

function Usage({ label, usage, limit, unlimited, bypass }: { label: string; usage: number; limit: number; unlimited: boolean; bypass?: boolean }) {
  const percent = limit > 0 ? quotaPercent(100 * usage / limit) + ' · ' : ''
  return <div>
    <div className="admin-usage-label"><strong>{label}</strong><span>{percent}{dollars(usage)} / {unlimited ? '∞' : limitDollars(limit)}{bypass && ' · Отключён'}</span></div>
    {limit > 0 && <progress aria-label={'Расход за ' + label} value={Math.min(usage, limit)} max={limit}/>}
  </div>
}

function AdminUserHistory({ admin, user, back }: { admin: ServerAdminAdapter; user: AdminUser; back: () => void }) {
  const [chats, setChats] = useState<AdminChat[] | null>(null)
  const [selected, setSelected] = useState<AdminChat | null>(null)
  const [messages, setMessages] = useState<ServerMessage[] | null>(null)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)
  const top = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!selected && chats) return
    const controller = new AbortController()
    const load = async () => {
      try {
        if (selected) {
          const result = await admin.messages(user.id, selected.id, controller.signal)
          if (!controller.signal.aborted) setMessages(result)
        } else {
          const result = await admin.chats(user.id, controller.signal)
          if (!controller.signal.aborted) setChats(result)
        }
      } catch (reason) {
        if (!controller.signal.aborted) setError(failure(reason))
      }
    }
    void load()
    return () => controller.abort()
  }, [admin, chats, selected, user.id, reload])

  function select(chat: AdminChat | null) {
    setError('')
    setMessages(null)
    setSelected(chat)
    top.current?.scrollIntoView({ block: 'nearest' })
  }

  const loading = selected ? messages === null : chats === null
  return <div ref={top} className="admin-history">
    <button type="button" className="alt-action" onClick={() => selected ? select(null) : back()}>{selected ? 'Назад к списку' : 'Назад к пользователям'}</button>
    <h3>{selected ? selected.title : 'Чаты · ' + user.username}</h3>
    {error ? <div className="notice" role="alert">{error} <button type="button" className="text-action" onClick={() => { setError(''); setReload(value => value + 1) }}>Повторить</button></div>
      : loading && <p className="form-help" role="status">Загрузка…</p>}
    {selected ? messages && (messages.length ? <ol className="admin-users">
      {messages.map(message => <li key={message.id} className="admin-user">
        <div className="admin-message-heading">
          <strong>{message.role === 'user' ? user.username : 'Агент'}</strong>
          <time>{dateTime.format(message.createdAt)}</time>
        </div>
        <RichText text={message.content}/>
      </li>)}
    </ol> : <p className="form-help">В чате пока нет сообщений.</p>) : chats && (chats.length ? <ul className="admin-users">
      {chats.map(chat => <li key={chat.id}><button type="button" className="alt-action admin-chat" onClick={() => select(chat)}>
        <strong>{chat.title}</strong><span className="form-help">Сообщений: {chat.messagesCount} · {date.format(chat.updatedAt)}</span>
      </button></li>)}
    </ul> : <p className="form-help">У пользователя пока нет чатов.</p>)}
  </div>
}
