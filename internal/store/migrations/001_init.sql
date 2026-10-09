-- doomctl: schema inicial (PostgreSQL 18)

CREATE TABLE users (
    id                   BIGSERIAL PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE,
    full_name            TEXT NOT NULL DEFAULT '',
    email                TEXT NOT NULL DEFAULT '',
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
    active               BOOLEAN NOT NULL DEFAULT TRUE,
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    mfa_enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    mfa_required         BOOLEAN NOT NULL DEFAULT FALSE,
    mfa_secret           BYTEA,
    mfa_pending_secret   BYTEA,
    mfa_last_step        BIGINT NOT NULL DEFAULT 0,
    mfa_recovery         JSONB NOT NULL DEFAULT '[]',
    failed_logins        INT NOT NULL DEFAULT 0,
    locked_until         TIMESTAMPTZ,
    last_login_at        TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,               -- sha256(token)
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('full','mfa','enroll')),
    csrf         TEXT NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions(user_id);

CREATE TABLE role_permissions (
    role       TEXT NOT NULL CHECK (role IN ('operator','viewer')),
    module     TEXT NOT NULL,
    can_read   BOOLEAN NOT NULL DEFAULT FALSE,
    can_manage BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (role, module)
);

CREATE TABLE tool_logs (
    id          BIGSERIAL PRIMARY KEY,
    module      TEXT NOT NULL,
    action      TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    username    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status IN ('queued','running','success','failed','canceled','interrupted','info')),
    exit_code   INT,
    input       JSONB NOT NULL DEFAULT '{}',
    output      TEXT NOT NULL DEFAULT '',
    result      JSONB,
    artifact    TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    duration_ms BIGINT
);
CREATE INDEX tool_logs_module_idx ON tool_logs(module, started_at DESC);
CREATE INDEX tool_logs_started_idx ON tool_logs(started_at DESC);

-- Ansible
CREATE TABLE ansible_hosts (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    port       INT NOT NULL DEFAULT 22,
    groups     TEXT[] NOT NULL DEFAULT '{}',
    vars       JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ansible_vaults (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    ciphertext  TEXT NOT NULL,                  -- formato $ANSIBLE_VAULT;1.1;AES256
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ansible_roles (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    tasks       TEXT NOT NULL DEFAULT '',
    handlers    TEXT NOT NULL DEFAULT '',
    meta        TEXT NOT NULL DEFAULT '',
    defaults    TEXT NOT NULL DEFAULT '',
    vars        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ansible_playbooks (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Docker
CREATE TABLE docker_files (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('dockerfile','compose')),
    name       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kind, name)
);

CREATE TABLE docker_registries (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    url           TEXT NOT NULL DEFAULT '',     -- vazio = Docker Hub
    username      TEXT NOT NULL DEFAULT '',
    password_enc  BYTEA,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- OpenTofu
CREATE TABLE tofu_projects (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    provider        TEXT NOT NULL CHECK (provider IN ('aws','oci')),
    description     TEXT NOT NULL DEFAULT '',
    files           JSONB NOT NULL DEFAULT '{}',
    credentials_enc BYTEA,
    plan_hash       TEXT NOT NULL DEFAULT '',
    plan_kind       TEXT NOT NULL DEFAULT '',
    plan_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Kubernetes (beta)
CREATE TABLE k8s_manifests (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE k8s_clusters (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    context        TEXT NOT NULL DEFAULT '',
    kubeconfig_enc BYTEA NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
