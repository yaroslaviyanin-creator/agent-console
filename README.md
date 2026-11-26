# Agent Console

Кроссплатформенный клиент удалённых агентов (веб, Telegram Mini App и десктоп на Tauri v2) с Go-бэкендом и хранением истории в SQLite (WAL).

## Возможности

- **Режим «Демо»**: работает полностью в памяти браузера без сервера (учебные аккаунты `demo`, `alex`, `maria` с паролем `demo`).
- **Режим «Сервер»**: авторизация, REST API, WebSocket-стриминг с тикетами, профили и память чатов, квоты, управление устройствами и админка.
- **Провайдеры**: `antigravity` (Antigravity CLI `agy`), `codex` (Codex CLI + чтение локальных чатов), `claude`, `paid_api` (и внутренний `stub` для dev/тестов).

---

## Быстрый старт в Docker (Windows / macOS / Linux)

Требуется установленный Docker Engine и Docker Compose v2+. Docker является основной средой запуска и проверки проекта: после внесения изменений в код необходимо пересобрать и перезапустить обновлённый сервис (`docker compose up -d --build web` для фронтенда, `docker compose up -d --build core runner` для бэкенда или `docker compose up -d --build` целиком).

1. Клонируйте репозиторий и создайте `.env` из шаблона:
   ```sh
   git clone https://github.com/yaroslaviyanin-creator/agent-console.git
   cd agent-console
   cp .env.example .env
   ```
   Сгенерируйте токен командой `openssl rand -hex 32` и запишите его в `RUNNER_SHARED_SECRET` в `.env`.

2. Соберите и запустите стек:
   ```sh
   docker compose up -d --build
   ```

3. Создайте первого пользователя (например, администратора `demo`):
   ```sh
   docker compose exec -it core /app/agent-server create-user --username demo --password 'demo-password' --role admin
   ```

4. Откройте веб-клиент:
   - Веб-интерфейс (Caddy): `http://127.0.0.1:3000`
   - Прямой адрес бэкенда (для Tauri и локальной разработки): `http://127.0.0.1:8080`
   - Проверка здоровья: `curl http://127.0.0.1:3000/api/health`

В форме входа выберите **«Сервер»**, укажите `http://127.0.0.1:3000` (или `http://127.0.0.1:8080`) и войдите под созданным логином и паролем.

### Подключение Antigravity CLI (`agy`) внутри Docker

1. В `.env` задайте `PROVIDER=antigravity` (и при необходимости `ANTIGRAVITY_MODEL=gemini-3.8-flash-high`).
2. Выполните `docker compose up -d --build core runner`. Для запуска с локальным прокси используйте профиль `antigravity` и параметры из [HANDOFF.md](HANDOFF.md#docker).
3. Выполните одноразовую авторизацию в контейнере `runner`:
   ```sh
   docker compose exec -it runner agy
   ```

### Горячий бэкап SQLite из volume

```sh
docker compose exec core sqlite3 /app/data/app.db ".backup /tmp/backup.db"
docker compose cp core:/tmp/backup.db ./backup.db
```

---

## Деплой на Linux VPS (HTTPS через Caddy)

1. Настройте DNS A-запись домена на IP сервера и откройте порты `80` и `443`.
2. В `.env` задайте `DOMAIN` и `RUNNER_SHARED_SECRET`. Prod-оверлей устанавливает CORS по указанному домену и запускает `unlocker`:
   ```env
   DOMAIN=console.example.com
   TRUST_PROXY_HEADERS=true
   ```
3. Запустите стек с prod-оверлеем:
   ```sh
   docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build
   ```
4. Создайте пользователя:
   ```sh
   docker compose exec -it core /app/agent-server create-user --username admin --password 'strong-password' --role admin
   ```

---

## Локальная разработка без Docker

Требуются Node.js 22+ и Go 1.26+.

```sh
# 1. Зависимости фронтенда
npm ci

# 2. Создание локального пользователя и запуск Go-сервера
cd server
go run ./cmd/server create-user --username demo --password 'demo-password' --role admin
SERVER_ADDR=127.0.0.1:8080 PROVIDER=stub go run ./cmd/server
```

В другом терминале из корня проекта:

```sh
npm run dev
```

Фронтенд откроется на `http://127.0.0.1:1420`.

### Проверки и тесты

```sh
npm run build
npm test
(cd server && go test ./...)
```

Подробная документация по архитектуре, переменным окружения и сборке Tauri v2 находится в [AGENTS.md](AGENTS.md) и [HANDOFF.md](HANDOFF.md).
