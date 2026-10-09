-- doomctl: módulo FinOps

-- Fontes de custo: AWS (Cost Explorer), OCI (Usage API), CSV e dados de exemplo.
CREATE TABLE finops_sources (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    provider        TEXT NOT NULL CHECK (provider IN ('aws','oci','csv','demo')),
    account_label   TEXT NOT NULL DEFAULT '',
    config          JSONB NOT NULL DEFAULT '{}',
    credentials_enc BYTEA,                    -- AES-256-GCM (secure.Box)
    currency        TEXT NOT NULL DEFAULT 'USD',
    auto_sync       BOOLEAN NOT NULL DEFAULT TRUE,
    last_sync_at    TIMESTAMPTZ,
    last_scan_at    TIMESTAMPTZ,
    last_status     TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT '',
    rightsizing     JSONB NOT NULL DEFAULT '[]', -- recomendações brutas do AWS Cost Explorer
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Custo diário por conta/serviço/região.
CREATE TABLE finops_costs (
    source_id  BIGINT NOT NULL REFERENCES finops_sources(id) ON DELETE CASCADE,
    day        DATE NOT NULL,
    provider   TEXT NOT NULL,
    account    TEXT NOT NULL DEFAULT '',
    service    TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    cost       NUMERIC(18,6) NOT NULL,
    currency   TEXT NOT NULL DEFAULT 'USD',
    usage_qty  NUMERIC(20,6) NOT NULL DEFAULT 0,
    usage_unit TEXT NOT NULL DEFAULT ''
);
CREATE INDEX finops_costs_day_idx ON finops_costs(day);
CREATE INDEX finops_costs_src_day_idx ON finops_costs(source_id, day);

-- Custo diário distribuído por tag (projeto, ambiente, equipe), mantendo o serviço.
CREATE TABLE finops_alloc (
    source_id BIGINT NOT NULL REFERENCES finops_sources(id) ON DELETE CASCADE,
    day       DATE NOT NULL,
    provider  TEXT NOT NULL,
    service   TEXT NOT NULL DEFAULT '',
    dim       TEXT NOT NULL CHECK (dim IN ('project','environment','team')),
    value     TEXT NOT NULL DEFAULT '',
    cost      NUMERIC(18,6) NOT NULL,
    currency  TEXT NOT NULL DEFAULT 'USD'
);
CREATE INDEX finops_alloc_day_idx ON finops_alloc(day);
CREATE INDEX finops_alloc_src_day_idx ON finops_alloc(source_id, day);

-- Custo diário por tipo de uso (rede e armazenamento).
CREATE TABLE finops_usage (
    source_id  BIGINT NOT NULL REFERENCES finops_sources(id) ON DELETE CASCADE,
    day        DATE NOT NULL,
    provider   TEXT NOT NULL,
    service    TEXT NOT NULL DEFAULT '',
    usage_type TEXT NOT NULL,
    category   TEXT NOT NULL DEFAULT '',
    cost       NUMERIC(18,6) NOT NULL,
    currency   TEXT NOT NULL DEFAULT 'USD',
    quantity   NUMERIC(20,6) NOT NULL DEFAULT 0,
    unit       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX finops_usage_day_idx ON finops_usage(day);
CREATE INDEX finops_usage_src_day_idx ON finops_usage(source_id, day);

-- Inventário de recursos (scan AWS, CSV ou exemplo).
CREATE TABLE finops_resources (
    id           BIGSERIAL PRIMARY KEY,
    source_id    BIGINT NOT NULL REFERENCES finops_sources(id) ON DELETE CASCADE,
    provider     TEXT NOT NULL,
    region       TEXT NOT NULL DEFAULT '',
    resource_id  TEXT NOT NULL,
    type         TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    sku          TEXT NOT NULL DEFAULT '',
    size_gb      DOUBLE PRECISION NOT NULL DEFAULT 0,
    state        TEXT NOT NULL DEFAULT '',
    monthly_cost DOUBLE PRECISION,
    currency     TEXT NOT NULL DEFAULT 'USD',
    cpu_avg      DOUBLE PRECISION,
    cpu_max      DOUBLE PRECISION,
    mem_avg      DOUBLE PRECISION,
    attached     BOOLEAN,
    age_days     INT NOT NULL DEFAULT 0,
    tags         JSONB NOT NULL DEFAULT '{}',
    raw          JSONB NOT NULL DEFAULT '{}',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_id, resource_id)
);

-- Catálogo de preços (estimativas para inventário, IaC, Kubernetes e cenários).
CREATE TABLE finops_prices (
    id         BIGSERIAL PRIMARY KEY,
    provider   TEXT NOT NULL,
    region     TEXT NOT NULL DEFAULT '*',
    sku        TEXT NOT NULL,
    unit       TEXT NOT NULL CHECK (unit IN ('hour','gb-month','month','gb-hour','unit')),
    price      NUMERIC(18,8) NOT NULL CHECK (price >= 0),
    currency   TEXT NOT NULL DEFAULT 'USD',
    source     TEXT NOT NULL DEFAULT 'manual',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, region, sku)
);

CREATE TABLE finops_budgets (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    scope_type  TEXT NOT NULL DEFAULT 'all',
    scope_value TEXT NOT NULL DEFAULT '',
    amount      NUMERIC(18,2) NOT NULL CHECK (amount > 0),
    currency    TEXT NOT NULL DEFAULT 'USD',
    thresholds  JSONB NOT NULL DEFAULT '[80,100]',
    notify      BOOLEAN NOT NULL DEFAULT TRUE,
    demo        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE finops_policies (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    match      TEXT NOT NULL DEFAULT '*',
    value      JSONB NOT NULL DEFAULT '{}',
    action     TEXT NOT NULL DEFAULT 'warn' CHECK (action IN ('warn','block')),
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    demo       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE finops_scenarios (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    items       JSONB NOT NULL DEFAULT '{}', -- {"a": [...], "b": [...]}
    demo        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE finops_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Recomendações descartadas pelos usuários.
CREATE TABLE finops_dismissed (
    key          TEXT PRIMARY KEY,
    reason       TEXT NOT NULL DEFAULT '',
    dismissed_by TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Alertas já enviados (deduplicação) e histórico.
CREATE TABLE finops_alerts (
    id        BIGSERIAL PRIMARY KEY,
    key       TEXT NOT NULL UNIQUE,
    kind      TEXT NOT NULL,
    severity  TEXT NOT NULL DEFAULT 'medium',
    message   TEXT NOT NULL,
    delivered BOOLEAN NOT NULL DEFAULT FALSE,
    error     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX finops_alerts_created_idx ON finops_alerts(created_at DESC);
