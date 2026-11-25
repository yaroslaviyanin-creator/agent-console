CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('pending', 'user', 'admin')),
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    plan_tier TEXT NOT NULL DEFAULT 'none' CHECK (plan_tier IN ('none', 'pro', '2x_pro', 'unlimited')),
    bypass_5h_limit INTEGER NOT NULL DEFAULT 0 CHECK (bypass_5h_limit IN (0, 1)),
    subscription_until INTEGER NOT NULL DEFAULT 0 CHECK (subscription_until >= 0),
    display_name TEXT NOT NULL DEFAULT '',
    profile_md TEXT NOT NULL DEFAULT '',
    memory_md TEXT NOT NULL DEFAULT '',
    memory_enabled INTEGER NOT NULL DEFAULT 1 CHECK (memory_enabled IN (0, 1)),
    memory_updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash BLOB NOT NULL UNIQUE,
    last_seen_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS chats (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('antigravity', 'codex', 'claude', 'paid_api', 'stub')),
    model TEXT NOT NULL,
    provider_session_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chat_messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    thinking TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    run_id TEXT,
    duration_ms INTEGER,
    run_user_id TEXT,
    memory_private INTEGER NOT NULL DEFAULT 1 CHECK (memory_private IN (0, 1))
);

CREATE TABLE IF NOT EXISTS chat_shares (
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access TEXT NOT NULL CHECK (access IN ('read', 'fork', 'write')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (chat_id, user_id)
);

CREATE TABLE IF NOT EXISTS usage_events (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_cents INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE (chat_id, run_id)
);

CREATE INDEX IF NOT EXISTS devices_by_user ON devices (user_id);
CREATE INDEX IF NOT EXISTS chats_by_user ON chats (user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS messages_by_chat ON chat_messages (chat_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS chat_message_runs ON chat_messages (chat_id, role, run_id);
CREATE INDEX IF NOT EXISTS chat_messages_by_role_time ON chat_messages (chat_id, role, created_at);
CREATE INDEX IF NOT EXISTS chat_messages_by_sender ON chat_messages (run_user_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS chat_shares_by_user ON chat_shares (user_id);
CREATE INDEX IF NOT EXISTS usage_events_by_user_time ON usage_events (user_id, created_at);
