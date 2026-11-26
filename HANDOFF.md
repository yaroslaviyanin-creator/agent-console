# Agent Console — передача контекста

## Статус

- React 19 / TypeScript / Vite / Zustand, Tauri v2 и опубликованный Telegram Mini App.
- Go-сервер поддерживает вход с ограничением частоты запросов, REST, WebSocket и восстановление истории. SQLite работает в режиме WAL. `MAX_CONCURRENT_STREAMS` ограничивает число одновременных потоков ответов.
- В режиме Server работает реальный `codex exec --json`, включая resume и динамический список моделей через `model/list`. Сквозной запрос из Telegram подтверждён.
- Поддерживается серверный провайдер `PROVIDER=antigravity` через локальный Antigravity CLI (`ANTIGRAVITY_BIN=agy`, модель `ANTIGRAVITY_MODEL=gemini-3.8-flash-high`). Провайдер использует авторизованный аккаунт Antigravity и его квоты, поддерживает сохранение и продолжение сессий (`--conversation`), потоковый вывод `stream-json`, sandbox-режим «Только чат» и каталог моделей через `GET /api/models` (`agy models`). Серверный реестр поддерживает Codex, Antigravity и Claude. `paid_api` показан в интерфейсе как недоступный. Чат закреплён за провайдером при создании.
- Поддерживаемые идентификаторы пользовательских провайдеров в протоколе и БД: `antigravity`, `codex`, `claude`, `paid_api` (плюс `stub` для dev/тестов).
- Раздел «Чаты Codex» показывает локальные обычные и архивные чаты владельца через Codex App Server. Проверены 46 чатов, включая 2 архивных. Импортированная история доступна для чтения.
- Для Telegram Mini App сервер доступен по постоянному HTTPS через Tailscale Funnel. [Agent Console Preview](https://t.me/AgentConsolePreview26Bot). Сборка для Sites размещена в отдельном checkout `telegram-demo-site/`.
- Go-сервер обслуживает режим «Только чат», включая общий доступ к чатам (`read`/`fork`/`write`, `PUT/DELETE /api/chats/{id}/shares/{userId}`, `POST /api/chats/{id}/fork`, `GET /api/users`), тарифные планы (`none`/`pro`/`2x_pro`/`unlimited`) и серверный учёт квот. Инструменты устройств (MCP) и переводы квот между пользователями остаются сценариями `MockTransport`.
- Серверные лимиты за 5 ч и 7 д суммируют стоимость успешных ответов из `usage_events`. Шапка получает текущую квоту через `GET /api/quotas/current`, а базовые лимиты Pro задаются `PRO_LIMIT_5H_CENTS` и `PRO_LIMIT_7D_CENTS`.

## Запуск из корня проекта

Создайте `.env` из `.env.example`, задайте случайный `RUNNER_SHARED_SECRET` командой `openssl rand -hex 32` и задайте `ENABLED_PROVIDERS=codex,antigravity,claude`. В старой конфигурации `PROVIDER` включает одного провайдера. Реальные CLI запускаются в `runner` через Docker Compose. Запуск Go API на хосте подходит для `stub`.

```sh
docker compose up -d --build
docker compose exec -it core /app/agent-server create-user --username demo --password 'demo-password'
npm ci
npm run build
npm test
(cd server && go test ./...)
```

Веб-клиент доступен на http://127.0.0.1:3000/, прямой локальный адрес API — http://127.0.0.1:8080. При локальном запуске стека на Mac для тестирования (включая Telegram Mini App) проверяйте `tailscale funnel status` и держите проброс `tailscale funnel --bg http://127.0.0.1:3000` на `https://macbook-pro.tail2aa739.ts.net` поверх порта Docker `web`, отдавая пользователю именно `https://macbook-pro.tail2aa739.ts.net`. При обновлении старого стека с `backend` один раз добавьте `--remove-orphans` к `docker compose up -d --build`. Имена существующих томов и формат сохранённых сессий сохранились. Для Telegram нужен доступный HTTPS-бэкенд и CORS с точным origin Mini App.

## Исправления истории Codex

- Go-парсер выносит вложения в `attachments`; интерфейс показывает их чипсами.
- `durationMs` отображается и без промежуточных сообщений. Для новых ответов значение записывается в SQLite и передаётся в WebSocket-событии `done`.

## Администрирование аккаунтов

- Роль `admin` выдаётся через `docker compose exec core /app/agent-server set-role --username ИМЯ --role admin`.
- После входа администратору доступна «Админка». Через неё можно создавать и блокировать аккаунты, менять роли, сбрасывать пароли и отзывать устройства.

## Устройства и WebSocket

- «Мои устройства» обновляет список через `GET /api/devices`. Отзыв через `DELETE /api/devices/{id}` завершает выбранную сессию. Выход отзывает токен текущего устройства через `POST /api/auth/logout` и очищает локальную сессию даже при недоступном сервере.
- Для каждого подключения клиент запрашивает одноразовый тикет через `POST /api/ws/ticket` с Bearer-токеном и передаёт тикет в `/ws?ticket=…`.

## Профиль, загрузки и память

- «Настройки» → «Профиль и память» сохраняет имя, аватар, предпочтения и память через `GET/PATCH /api/profile`. В общие чаты передаётся имя автора сообщения (`avatarData` и метаданные файлов никогда не попадают в инструкции LLM).
- Любая загрузка файлов и картинок в чат идёт строго через `server/internal/uploads` (`POST /api/chats/{id}/attachments`, `GET /api/attachments/{id}`, `DELETE /api/attachments/{id}`), файлы хранятся в `/app/data/uploads/<uuid>` с правами `0600`. Растровые картинки перекодируются в чистый JPEG/PNG (`codex` принимает их через `-i` во временном `/tmp/req-*`, `claude` через `--input-format stream-json`, `antigravity` при 0 инструментов их отклоняет с понятной ошибкой). `.html`/`.svg`/`.xml` и исходники отдаются строго как `text/plain` attachment; `.zip` читаются только в памяти (150 записей, 5 МБ, без распаковки на диск и с защитой от Zip Slip / zip-бомб); `.pdf` парсятся в памяти в `core`.
- Фоновое сжатие личных чатов через CLI регулируют `MEMORY_AUTO_UPDATE` (true), `MEMORY_MODEL` (default), `MEMORY_CHECK_INTERVAL` (5m) и `MEMORY_MIN_NEW_MESSAGES` (6). Запуски используют общий лимит потоков и не записываются в квоту клиента.

## Docker

- **core** хранит `/app/data/app.db` в `app_data`, обслуживает API и WebSocket. Образ содержит `agent-server` и `sqlite3`. Caddy проксирует `/api/*` и `/ws` на `core:8080`.
- **runner** запускает CLI из пустой `/workspace`. Корневая ФС и workspace доступны только для чтения, `/tmp` — временный. Тома хранят авторизацию и сессии CLI, включая существующий `cli_codex_chat`. У сервиса нет тома БД или файла `.env`. Внутренние HTTP-запросы авторизуются общим с `core` токеном. NDJSON передаёт дельты, usage и ID сессии, отмена HTTP-запроса останавливает генерацию. Непустой `mcp_endpoint` зарезервирован для будущего одноразового MCP-моста и пока отклоняется.
- **unlocker** получает только относящиеся к нему настройки, включая `AG_UNLOCKER_CODE` и том `ag_unlocker_data`. Supervisor перезапускает `ag_unlocker --proxy` и его TCP-переадресацию с loopback в отдельную сеть Compose. Runner использует `AG_LS_PROXY=http://unlocker:53130`. Порт наружу не публикуется.
- Локально `docker compose up -d --build` собирает `core` и `runner` нативно на Apple Silicon. Для Antigravity с прокси выполните `PROVIDER=antigravity AGY_USE_UNLOCKER=true AG_LS_PROXY=http://unlocker:53130 docker compose --profile antigravity up -d --build`. В этом профиле только `unlocker` работает как `linux/amd64`.
- Для однократной авторизации CLI используйте `docker compose exec -e CODEX_HOME=/home/app/.codex-chat -it runner codex login --device-auth`, `docker compose exec -it runner agy` или `docker compose exec -it runner claude`. Сессии CLI хранятся в их Docker-томах.
- `ENABLED_PROVIDERS` — список через запятую из `codex`, `antigravity`, `claude` и тестового `stub`. `DEFAULT_PROVIDER` по умолчанию берётся первым из списка, `MEMORY_PROVIDER` по умолчанию совпадает с `DEFAULT_PROVIDER`. Старый `PROVIDER` используется, когда список не задан. Реестр и выбор по умолчанию доступны в `GET /api/providers`, каталог — в `GET /api/models?provider=...`. Переключение провайдера в интерфейсе создаёт отдельный чат.
- Горячий бэкап SQLite:
  ```sh
  docker compose exec core sqlite3 /app/data/app.db ".backup /tmp/backup.db"
  docker compose cp core:/tmp/backup.db ./backup.db
  ```
- На Linux VPS задайте `DOMAIN`, `RUNNER_SHARED_SECRET` и при необходимости `AG_UNLOCKER_CODE` в `.env`. Команда `docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build` включает unlocker по умолчанию, публикует только Caddy на 80/443 и задаёт `CORS_ORIGIN=https://${DOMAIN}`.
