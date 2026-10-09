-- doomctl: configuração dos agentes de IA pelo navegador

-- Configuração ativa (chave 'settings' = agents.Settings em JSON).
CREATE TABLE ai_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Histórico de versões da configuração (restauração com um clique).
CREATE TABLE ai_settings_history (
    id         BIGSERIAL PRIMARY KEY,
    value      JSONB NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ai_settings_history_created_idx ON ai_settings_history(created_at DESC);

-- Conexões com provedores de modelos: Ollama remoto, APIs compatíveis com OpenAI
-- (OpenAI, Azure OpenAI, Microsoft Foundry, Gemini, OpenRouter, vLLM...) e agentes do Foundry.
CREATE TABLE ai_connections (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('ollama','openai','foundry_agent')),
    preset       TEXT NOT NULL DEFAULT '',
    config       JSONB NOT NULL DEFAULT '{}',
    secret_enc   BYTEA,                      -- chave/token/client secret (AES-256-GCM, secure.Box)
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    last_test_at TIMESTAMPTZ,
    last_status  TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Servidores MCP (Model Context Protocol, transporte Streamable HTTP).
CREATE TABLE ai_mcp_servers (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    url              TEXT NOT NULL,
    preset           TEXT NOT NULL DEFAULT '',
    config           JSONB NOT NULL DEFAULT '{}',
    secret_enc       BYTEA,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    tools            JSONB NOT NULL DEFAULT '[]',
    server_info      JSONB NOT NULL DEFAULT '{}',
    tools_updated_at TIMESTAMPTZ,
    last_status      TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
