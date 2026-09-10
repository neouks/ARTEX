-- ARTEX PostgreSQL schema (单一数据源)
-- 幂等：可重复执行（IF NOT EXISTS / OR REPLACE / DROP TRIGGER IF EXISTS）。

-- =====================================================================
-- 0. 通用：updated_at 触发器
-- =====================================================================
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

-- 安全的 text→inet 转换：非法值返回 NULL 而不是抛 22P02。assets.ip 是自由文本
-- (Agent / 资产 API 可能写进主机名)，裸转 a.ip::inet 会让单独一行脏数据把整条
-- 企业归属重算语句打挂。调用方用 try_inet(...) IS NULL 找出这些行并告警。
-- 不用 pg_input_is_valid 是因为那要 PG16+，这里要兼容更老的存量库。
CREATE OR REPLACE FUNCTION try_inet(value text) RETURNS inet AS $$
BEGIN
    RETURN value::inet;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE STRICT;

-- =====================================================================
-- A. 资产层：companies / assets / company_scope
-- =====================================================================

CREATE TABLE IF NOT EXISTS companies (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    nkey       TEXT NOT NULL UNIQUE,
    logo       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_companies_nkey ON companies(nkey);
DROP TRIGGER IF EXISTS trg_companies_upd ON companies;
CREATE TRIGGER trg_companies_upd BEFORE UPDATE ON companies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS assets (
    id              BIGSERIAL PRIMARY KEY,
    type            TEXT NOT NULL CHECK (type IN (
                        'root_domain','ip','subdomain','app','service','endpoint'
                    )),
    company_id      BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    -- explicit: caller/user selected the company; scope: derived from company_scope.
    -- Existing installations are conservatively migrated as explicit so a scope
    -- rebuild can never erase a historical manual association.
    company_source  TEXT NOT NULL DEFAULT 'explicit'
                    CHECK (company_source IN ('explicit','scope')),
    task_ids        BIGINT[] NOT NULL DEFAULT '{}',
    domain          TEXT,
    root_domain     TEXT,
    ip              TEXT,
    c_segment       CIDR,
    port            INTEGER CHECK (port BETWEEN 1 AND 65535),
    icp             TEXT,
    bound_domains   TEXT[]  NOT NULL DEFAULT '{}',
    open_ports      JSONB[] NOT NULL DEFAULT '{}',
    record_type     TEXT,
    record_value    TEXT[],
    bundle_id       TEXT,
    app_name        TEXT,
    category        TEXT,
    app_description TEXT,
    app_icp         TEXT,
    url             TEXT,
    service_type    TEXT CHECK (service_type IN ('http','other')),
    service_name    TEXT,
    favicon_mmh3    TEXT,
    status_code     INTEGER,
    content_length  BIGINT,
    page_title      TEXT,
    technologies    TEXT[]  NOT NULL DEFAULT '{}',
    auth            JSONB[] NOT NULL DEFAULT '{}',
    method          TEXT,
    params          JSONB[] NOT NULL DEFAULT '{}',
    extra           JSONB   NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_root_domain  ON assets(domain) WHERE type = 'root_domain';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_ip           ON assets(ip)     WHERE type = 'ip';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_subdomain    ON assets(domain, COALESCE(record_type,'')) WHERE type = 'subdomain';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_bundle   ON assets(bundle_id) WHERE type = 'app' AND bundle_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_name     ON assets(app_name)  WHERE type = 'app' AND bundle_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_http ON assets(url) WHERE type = 'service' AND service_type = 'http';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_other
    ON assets(COALESCE(domain,''), COALESCE(ip,''), port, service_name) WHERE type = 'service' AND service_type = 'other';
CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_endpoint     ON assets(url, method) WHERE type = 'endpoint';
CREATE INDEX IF NOT EXISTS idx_av2_company      ON assets(company_id)       WHERE company_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_company_type ON assets(company_id, type) WHERE company_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_task_ids     ON assets USING GIN(task_ids);
CREATE INDEX IF NOT EXISTS idx_av2_domain       ON assets(domain)      WHERE domain IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_root_domain  ON assets(root_domain) WHERE root_domain IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_ip           ON assets(ip)          WHERE ip IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_c_segment    ON assets USING GIST(c_segment inet_ops) WHERE c_segment IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_av2_technologies ON assets USING GIN(technologies) WHERE type = 'service';
CREATE INDEX IF NOT EXISTS idx_av2_bound_domains ON assets USING GIN(bound_domains) WHERE type = 'ip';
CREATE INDEX IF NOT EXISTS idx_av2_open_ports   ON assets USING GIN(open_ports)    WHERE type = 'ip';
CREATE INDEX IF NOT EXISTS idx_av2_last_seen    ON assets(last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_av2_type_seen    ON assets(type, last_seen DESC);
ALTER TABLE assets ADD COLUMN IF NOT EXISTS company_source TEXT;
UPDATE assets SET company_source = 'explicit' WHERE company_source IS NULL;
ALTER TABLE assets ALTER COLUMN company_source SET DEFAULT 'explicit';
ALTER TABLE assets ALTER COLUMN company_source SET NOT NULL;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_company_source_check;
ALTER TABLE assets ADD CONSTRAINT assets_company_source_check
    CHECK (company_source IN ('explicit','scope'));
DROP TRIGGER IF EXISTS trg_av2_upd ON assets;
CREATE TRIGGER trg_av2_upd BEFORE UPDATE ON assets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS company_scope (
    id         BIGSERIAL PRIMARY KEY,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('domain','ip','cidr','icp','keyword')),
    domain     TEXT,
    net        CIDR,
    value      TEXT,
    raw        TEXT NOT NULL,
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain),
    CONSTRAINT uq_sv2_net    UNIQUE (company_id, net),
    CONSTRAINT ck_company_scope_payload CHECK (
        (kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL)
        OR (kind IN ('ip','cidr') AND domain IS NULL AND net IS NOT NULL AND value IS NULL)
        OR (kind IN ('icp','keyword') AND domain IS NULL AND net IS NULL AND value IS NOT NULL)
    )
);
-- Existing installations need the new text payload and expanded kind check.
ALTER TABLE company_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS company_scope_kind_check;
ALTER TABLE company_scope ADD CONSTRAINT company_scope_kind_check
    CHECK (kind IN ('domain','ip','cidr','icp','keyword'));
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS ck_company_scope_payload;
ALTER TABLE company_scope ADD CONSTRAINT ck_company_scope_payload CHECK (
    (kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL)
    OR (kind IN ('ip','cidr') AND domain IS NULL AND net IS NOT NULL AND value IS NULL)
    OR (kind IN ('icp','keyword') AND domain IS NULL AND net IS NULL AND value IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_sv2_domain  ON company_scope(domain)   WHERE kind = 'domain';
CREATE INDEX IF NOT EXISTS idx_sv2_net     ON company_scope USING GIST(net inet_ops) WHERE kind IN ('ip','cidr');
DROP INDEX IF EXISTS uq_sv2_value;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sv3_value ON company_scope(company_id, kind, md5(value)) WHERE kind IN ('icp','keyword');
DROP INDEX IF EXISTS idx_sv2_icp;
CREATE INDEX IF NOT EXISTS idx_sv3_icp ON company_scope(md5(value)) WHERE kind = 'icp';
CREATE INDEX IF NOT EXISTS idx_sv2_company ON company_scope(company_id);

-- =====================================================================
-- B. 推理探索层
-- =====================================================================
CREATE TABLE IF NOT EXISTS explorations (
    id          BIGSERIAL PRIMARY KEY,
    description TEXT,
    goal        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open','achieved','failed')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- cold-digest (§2.3): per-task planner round counter — bumped once each time the
-- planner wakes and processes a round. Drives the ≥R cold-node debounce (measured in
-- this exploration's own rounds, not global node ids or wall-clock).
ALTER TABLE explorations ADD COLUMN IF NOT EXISTS round_no BIGINT NOT NULL DEFAULT 0;
DROP TRIGGER IF EXISTS trg_exp_upd ON explorations;
CREATE TRIGGER trg_exp_upd BEFORE UPDATE ON explorations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS exploration_nodes (
    id             BIGSERIAL PRIMARY KEY,
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL,
    payload        JSONB NOT NULL DEFAULT '{}',
    priority       INT  NOT NULL DEFAULT 0,
    state          TEXT NOT NULL DEFAULT 'open',
    origin         TEXT,
    owner          TEXT,
    blocked_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ,
    CONSTRAINT ck_node_kind CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest')),
    CONSTRAINT ck_node_state CHECK (
        (kind='begin'   AND state IN ('open')) OR
        (kind='intent'  AND state IN ('open','running','paused','done','blocked','exhausted','stopped')) OR
        (kind='goal'    AND state IN ('open','met','abandoned')) OR
        (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
        (kind='finding' AND state IN ('confirmed','dismissed')) OR
        (kind='hint'    AND state IN ('active','consumed')) OR
        (kind='digest'  AND state IN ('active','superseded'))
    )
);
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS blocked_reason TEXT;
-- cold-digest (§2.3/§5.3): content_version bumps on any change that could alter a
-- digest body (summary/state/confidence); cold_since_round stamps the planner round
-- a node most recently went from "has a live downstream branch" to none (NULL = hot).
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS content_version  INT    NOT NULL DEFAULT 0;
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS cold_since_round BIGINT;
-- ck_node_kind: existing installs predate the 'digest' kind — recreate to allow it.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_kind'
          AND pg_get_constraintdef(oid) NOT LIKE '%digest%'
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_kind;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_kind
            CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest'));
    END IF;
END $$;
-- ck_node_state: recreate when it lacks the 'paused' (older) or 'digest' (this rev) branches.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_state'
          AND (pg_get_constraintdef(oid) NOT LIKE '%paused%'
               OR pg_get_constraintdef(oid) NOT LIKE '%superseded%')
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_state;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_state CHECK (
            (kind='begin'   AND state IN ('open')) OR
            (kind='intent'  AND state IN ('open','running','paused','done','blocked','exhausted','stopped')) OR
            (kind='goal'    AND state IN ('open','met','abandoned')) OR
            (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
            (kind='finding' AND state IN ('confirmed','dismissed')) OR
            (kind='hint'    AND state IN ('active','consumed')) OR
            (kind='digest'  AND state IN ('active','superseded'))
        );
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_expnodes_part     ON exploration_nodes(exploration_id, kind);
CREATE INDEX IF NOT EXISTS idx_expnodes_frontier ON exploration_nodes(exploration_id, priority DESC)
    WHERE kind='intent' AND state='open';
DROP TRIGGER IF EXISTS trg_expnodes_upd ON exploration_nodes;
CREATE TRIGGER trg_expnodes_upd BEFORE UPDATE ON exploration_nodes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS exploration_edges (
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    src_id         BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    dst_id         BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    rel            TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (exploration_id, src_id, rel, dst_id),
    CONSTRAINT ck_edge_noself CHECK (src_id <> dst_id),
    CONSTRAINT ck_edge_rel CHECK (rel IN ('spawns','derived_from','yields','proves','covers'))
);
CREATE INDEX IF NOT EXISTS idx_expedges_src ON exploration_edges(src_id, rel);
CREATE INDEX IF NOT EXISTS idx_expedges_dst ON exploration_edges(dst_id, rel);
-- cold-digest (§1): the 'covers' relation (digest→member) postdates shipped installs,
-- whose rel CHECK is an inline auto-named constraint. Find and recreate it as ck_edge_rel.
DO $$
DECLARE cname text;
BEGIN
    SELECT conname INTO cname FROM pg_constraint
     WHERE conrelid='exploration_edges'::regclass AND contype='c'
       AND pg_get_constraintdef(oid) LIKE '%rel%'
       AND pg_get_constraintdef(oid) NOT LIKE '%covers%';
    IF cname IS NOT NULL THEN
        EXECUTE 'ALTER TABLE exploration_edges DROP CONSTRAINT '||quote_ident(cname);
        ALTER TABLE exploration_edges ADD CONSTRAINT ck_edge_rel
            CHECK (rel IN ('spawns','derived_from','yields','proves','covers'));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS exploration_anchors (
    node_id   BIGINT NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    asset_id  BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    PRIMARY KEY (node_id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_anchor_asset ON exploration_anchors(asset_id);

-- task_constraints: operator-authored operation constraints (allow/deny) for a task.
-- Extracted by the goals decomposer at round 0 (from goal/description), editable at
-- runtime by the main agent + 总览「约束管理」. Injected into the planner/worker system
-- prompt each round (config-gated) to keep exploration within the operator's boundary.
CREATE TABLE IF NOT EXISTS task_constraints (
    id             BIGSERIAL PRIMARY KEY,
    exploration_id BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('allow','deny')),
    text           TEXT NOT NULL,
    origin         TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_constraints_exp ON task_constraints(exploration_id);

CREATE TABLE IF NOT EXISTS activity (
    id                 BIGSERIAL PRIMARY KEY,
    exploration_id     BIGINT NOT NULL REFERENCES explorations(id) ON DELETE CASCADE,
    node_id            BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    worker             TEXT,
    kind               TEXT,
    tool               TEXT,
    tool_use_id        TEXT,
    is_error           BOOLEAN NOT NULL DEFAULT false,
    summary            TEXT,
    detail             TEXT,
    metadata           JSONB NOT NULL DEFAULT '{}',
    input_tokens       INTEGER,
    output_tokens      INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE activity ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_act_node  ON activity(exploration_id, node_id, id);
CREATE INDEX IF NOT EXISTS idx_act_since ON activity(exploration_id, id);
-- Main/Plan history pages filter by worker (both carry NULL node_id, so idx_act_node
-- can't distinguish them); this covers reverse pagination of those sessions.
CREATE INDEX IF NOT EXISTS idx_act_worker ON activity(exploration_id, worker, id);
-- Task-list polls aggregate result usage and find the latest event repeatedly.
-- Cover the token columns for index-only aggregation and the timestamp order for
-- per-exploration latest-activity lookups.
CREATE INDEX IF NOT EXISTS idx_act_result_usage ON activity(exploration_id)
    INCLUDE (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
    WHERE kind='result';
CREATE INDEX IF NOT EXISTS idx_act_latest ON activity(exploration_id, created_at DESC);

-- =====================================================================
-- C. LLM profiles
-- =====================================================================
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS llm_profiles (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    format           TEXT NOT NULL CHECK (format IN ('openai','anthropic','openai-responses')),
    base_url         TEXT,
    proxy            TEXT,
    model            TEXT NOT NULL,
    api_key          TEXT,
    api_key_hint     TEXT,
    rate_per_second  DOUBLE PRECISION NOT NULL DEFAULT 0,
    rate_per_minute  DOUBLE PRECISION NOT NULL DEFAULT 0,
    context_window_k INTEGER NOT NULL DEFAULT 0,
    -- 思考参数拆成两个独立字段：thinking_type=思考开关(''/disabled/enabled)，
    -- reasoning_effort=思考强度(''/low/medium/high/xhigh/max)，互不牵连。
    reasoning_effort TEXT NOT NULL DEFAULT '',
    thinking_type    TEXT NOT NULL DEFAULT '',
    is_default       BOOLEAN NOT NULL DEFAULT false,
    -- 轮询(故障转移)参数，见 docs/LLM轮询设计.md：
    --   priority     顺位，越大越先被选中；激活配置(is_default)永远排链首，与本值无关。
    --   pool_exclude true=不作为故障转移目标(仍可被 agent/任务显式绑定使用)。
    priority         INTEGER NOT NULL DEFAULT 0,
    pool_exclude     BOOLEAN NOT NULL DEFAULT false,
    -- streaming=true(默认)走流式 SSE；false 走真·非流式(stream:false，一次性 JSON)。
    streaming        BOOLEAN NOT NULL DEFAULT true,
    -- 单次回复的输出上限(token)。0=不发送该字段，由服务端默认值决定——保持既有行为。
    -- 与 context_window_k(模型总容量，仅本地用于压缩阈值)是两回事：本值会随请求发出。
    max_tokens       INTEGER NOT NULL DEFAULT 0,
    -- 输出上限用哪个请求字段名，仅对 format='openai' 生效：
    --   ''                      = max_tokens(默认，兼容绝大多数网关)
    --   'max_completion_tokens' = 新字段；OpenAI 推理模型(o 系列/GPT-5)只认它，
    --                             发 max_tokens 会被 unsupported_parameter 拒绝。
    -- anthropic(max_tokens 必填)与 openai-responses(max_output_tokens)自带字段名，不受此值影响。
    max_tokens_field TEXT NOT NULL DEFAULT '',
    -- 自定义会话头：非空时每次请求带一个该名字的 HTTP 头，头值=当前运行的 session id
    -- (chat 会话/worker 意图)。用于某些按 session-id 头做提示缓存/粘性路由的网关。''=不发送。
    session_header_key TEXT NOT NULL DEFAULT '',
    -- 重试覆盖：次数 0=用全局默认/-1=关闭/>0=该值；间隔 0=用默认指数退避/>0=固定毫秒。
    -- 三组分别对应建连重试、空响应重试、同 provider 安全窗口重试，详见下方 ALTER 处注释。
    retry_connect_attempts    INTEGER NOT NULL DEFAULT 0,
    retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0,
    retry_empty_attempts      INTEGER NOT NULL DEFAULT 0,
    retry_empty_interval_ms   INTEGER NOT NULL DEFAULT 0,
    retry_stream_attempts     INTEGER NOT NULL DEFAULT 0,
    retry_stream_interval_ms  INTEGER NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_one_default ON llm_profiles(is_default) WHERE is_default;
DROP TRIGGER IF EXISTS trg_llm_upd ON llm_profiles;
CREATE TRIGGER trg_llm_upd BEFORE UPDATE ON llm_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- 轮询顺位/排除标记；补旧库。默认 0 / false = 全部配置都参与轮询。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS priority     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS pool_exclude BOOLEAN NOT NULL DEFAULT false;
-- 流式开关；补旧库。默认 true = 保持既有的流式行为，旧配置无感升级。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS streaming    BOOLEAN NOT NULL DEFAULT true;
-- 放开 format 约束以容纳 openai-responses(OpenAI Responses API)；补旧库。
-- 每次启动执行,幂等:先删旧 CHECK 再建含三值的新 CHECK。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_format_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_format_check
    CHECK (format IN ('openai','anthropic','openai-responses'));

-- 输出上限及其字段名；补旧库。默认 0 / '' = 不发送上限、沿用 max_tokens 字段名，
-- 旧配置行为完全不变。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens_field TEXT    NOT NULL DEFAULT '';
-- 同 format：先删再建，保证每次启动幂等。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_field_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_field_check
    CHECK (max_tokens_field IN ('','max_completion_tokens'));
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_check
    CHECK (max_tokens >= 0);
-- 自定义会话头名；补旧库。默认 '' = 不发送，旧配置行为不变。
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS session_header_key TEXT NOT NULL DEFAULT '';

-- 单配置的重试覆盖（见 docs/LLM重试设计.md）。三组各自一对「次数 + 固定间隔」，
-- 语义统一：次数 0=沿用全局默认、-1=关闭该层重试、>0=用该值；间隔 0=沿用该层的
-- 默认指数退避、>0=改用这个固定毫秒数。全部默认 0，所以旧库/旧配置行为不变。
--   connect = 建连重试（SDK doStream：连接重置/超时/429/5xx，流开始前）
--   empty   = 空响应重试（SDK：完成但没有任何 content block，仅 openai 格式）
--   stream  = 同 provider 安全窗口重试（本项目 task_llm：未交付输出前的断流重放）
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_attempts    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_attempts      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_interval_ms   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_attempts     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_interval_ms  INTEGER NOT NULL DEFAULT 0;
-- 同 format：先删再建，保证每次启动幂等。次数下限 -1(关闭)，间隔不能为负。
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_retry_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_retry_check CHECK (
    retry_connect_attempts >= -1 AND retry_empty_attempts >= -1 AND retry_stream_attempts >= -1
    AND retry_connect_interval_ms >= 0 AND retry_empty_interval_ms >= 0 AND retry_stream_interval_ms >= 0);

-- 思考开关字段 thinking_type，从旧的单一 reasoning_effort 语义一次性拆分而来。
-- schema.sql 每次启动都执行，故迁移必须只跑一次：仅当该列尚不存在时才回填，
-- 否则每次启动都会把用户后来手动设的组合覆盖回去。旧 reasoning_effort 语义：
--   'off'                    → 显式关闭  → thinking_type='disabled'，强度清空
--   'low/medium/high/max'    → 开启+强度 → thinking_type='enabled'，强度保留
--   ''                       → 不发送    → 两者皆空(默认)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'llm_profiles' AND column_name = 'thinking_type'
    ) THEN
        ALTER TABLE llm_profiles ADD COLUMN thinking_type TEXT NOT NULL DEFAULT '';
        UPDATE llm_profiles SET thinking_type = 'enabled'
            WHERE reasoning_effort IN ('low','medium','high','max');
        UPDATE llm_profiles SET thinking_type = 'disabled', reasoning_effort = ''
            WHERE reasoning_effort = 'off';
    END IF;
END $$;

-- LLM 轮询熔断状态：某个配置连续失败(余额不足/key 失效/限流)后进入冷却，冷却期内
-- 轮询直接跳过它。内存态为准，这里落库只为重启后不丢冷却窗口——加载时只取尚未
-- 到期的行(open_until > now)，已到期的自然回到"正常"，等下一次调用半开试探。
CREATE TABLE IF NOT EXISTS llm_profile_health (
    profile_id  BIGINT PRIMARY KEY REFERENCES llm_profiles(id) ON DELETE CASCADE,
    fails       INTEGER NOT NULL DEFAULT 0,  -- 当前连续失败次数(成功即清零)
    trips       INTEGER NOT NULL DEFAULT 0,  -- 累计熔断次数,用于冷却时间指数退避
    open_until  TIMESTAMPTZ,                 -- 冷却截止;NULL/过期 = 未熔断
    last_error  TEXT NOT NULL DEFAULT '',
    last_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- =====================================================================
-- D. 任务层
-- =====================================================================
-- Global task categories are intentionally independent from task templates.
-- Deleting a category only moves its tasks back to the uncategorized bucket.
CREATE TABLE IF NOT EXISTS task_categories (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    nkey       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_categories_name ON task_categories(name, id);
DROP TRIGGER IF EXISTS trg_task_categories_upd ON task_categories;
CREATE TRIGGER trg_task_categories_upd BEFORE UPDATE ON task_categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS tasks (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL DEFAULT '',
    category_id    BIGINT REFERENCES task_categories(id) ON DELETE SET NULL,
    description    TEXT NOT NULL,
    goal           TEXT NOT NULL,
    exploration_id BIGINT NOT NULL UNIQUE
                     REFERENCES explorations(id) ON DELETE RESTRICT,
    status         TEXT NOT NULL DEFAULT 'created'
                     CHECK (status IN ('created','running','paused','done','failed','timeout')),
    paused         BOOLEAN NOT NULL DEFAULT false,
    queued         BOOLEAN NOT NULL DEFAULT false,
    queued_at      TIMESTAMPTZ,
    queue_mode     TEXT NOT NULL DEFAULT '',
    llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    active_llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    llm_chain_revision BIGINT NOT NULL DEFAULT 0,
    company_id     BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    parent_ref     TEXT,
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300,
    coverage_enabled BOOLEAN NOT NULL DEFAULT true,
    pinned_at      TIMESTAMPTZ,
    first_run_at   TIMESTAMPTZ,
    deadline_at    TIMESTAMPTZ,
    archived_at    TIMESTAMPTZ,
    deleted_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tasks_alive  ON tasks(created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)          WHERE deleted_at IS NULL;
DROP TRIGGER IF EXISTS trg_tasks_upd ON tasks;
CREATE TRIGGER trg_tasks_upd BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- planner 心跳触发间隔(秒);补旧库。默认 300s(5min)。见 docs/planner-trigger-impl-plan.md
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300;
-- 并发上限挂起态;补旧库。true=因并发上限排队、等待空位自动启动。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued BOOLEAN NOT NULL DEFAULT false;
-- 资产覆盖度功能开关;补旧库。true(默认)=计算/展示测试覆盖度、自动累积测试范围、
-- 给 agent 开放 add_task_scope/list_untested_assets;false=全部关闭(见 task_scope.go)。
-- 存量任务默认 true 保持原行为;company 关联(task_scope kind=company)不受此开关影响。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS coverage_enabled BOOLEAN NOT NULL DEFAULT true;
-- queued_at makes admission FIFO reflect the actual enqueue order rather than the
-- task creation order. queue_mode distinguishes first bootstrap from resuming an
-- exploration that already owns goals/history.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queue_mode TEXT NOT NULL DEFAULT '';
-- 可选的任务名称;补旧库。空串=未命名,前端展示时回退到描述。
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS category_id BIGINT REFERENCES task_categories(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS active_llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS llm_chain_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_tasks_category ON tasks(category_id, created_at DESC)
    WHERE deleted_at IS NULL AND category_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_pinned ON tasks(pinned_at DESC)
    WHERE deleted_at IS NULL AND pinned_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_archived ON tasks(archived_at DESC)
    WHERE archived_at IS NOT NULL;

-- Cold task archives retain only compact metadata in PostgreSQL. The complete
-- task payload lives in a versioned .tar.zst package under data/archives/tasks.
-- task_id stays unique so an operation can be retried safely after a restart.
CREATE TABLE IF NOT EXISTS task_archives (
    id                         BIGSERIAL PRIMARY KEY,
    task_id                    BIGINT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    state                      TEXT NOT NULL DEFAULT 'archive_queued' CHECK (state IN (
                                   'archive_queued','archiving','archive_failed','ready',
                                   'restore_queued','restoring','restore_failed',
                                   'delete_queued','deleting','delete_failed'
                               )),
    phase                      TEXT NOT NULL DEFAULT 'queued',
    progress                   INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    error                      TEXT NOT NULL DEFAULT '',
    warnings                   JSONB NOT NULL DEFAULT '[]',
    format_version             INTEGER NOT NULL DEFAULT 2,
    archive_path               TEXT NOT NULL DEFAULT '',
    sha256                     TEXT NOT NULL DEFAULT '',
    original_size              BIGINT NOT NULL DEFAULT 0,
    compressed_size            BIGINT NOT NULL DEFAULT 0,
    task_name                  TEXT NOT NULL DEFAULT '',
    task_description           TEXT NOT NULL DEFAULT '',
    task_goal                  TEXT NOT NULL DEFAULT '',
    original_status            TEXT NOT NULL DEFAULT '',
    category_id_snapshot       BIGINT,
    category_name_snapshot     TEXT NOT NULL DEFAULT '',
    source_task_ids            BIGINT[] NOT NULL DEFAULT '{}',
    remaining_timeout_seconds  BIGINT NOT NULL DEFAULT 0,
    data_counts                JSONB NOT NULL DEFAULT '{}',
    aggregate_stats            JSONB NOT NULL DEFAULT '{}',
    archived_at                TIMESTAMPTZ,
    requested_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE task_archives ALTER COLUMN format_version SET DEFAULT 2;
CREATE INDEX IF NOT EXISTS idx_task_archives_state ON task_archives(state, requested_at, id);
CREATE INDEX IF NOT EXISTS idx_task_archives_archived ON task_archives(archived_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_task_archives_sources ON task_archives USING GIN(source_task_ids);
DROP TRIGGER IF EXISTS trg_task_archives_upd ON task_archives;
CREATE TRIGGER trg_task_archives_upd BEFORE UPDATE ON task_archives
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Reusable task description/goal presets. nkey is the normalized, case-insensitive
-- identity used to reject visually equivalent duplicate names.
CREATE TABLE IF NOT EXISTS task_templates (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    nkey        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    goal        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_templates_updated ON task_templates(updated_at DESC, id DESC);
DROP TRIGGER IF EXISTS trg_task_templates_upd ON task_templates;
CREATE TRIGGER trg_task_templates_upd BEFORE UPDATE ON task_templates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Direct, read-only task context inheritance. Relations are intentionally not
-- recursive: a task sees only the source tasks explicitly chosen at creation.
CREATE TABLE IF NOT EXISTS task_relations (
    task_id        BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, source_task_id),
    CONSTRAINT ck_task_relation_not_self CHECK (task_id <> source_task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_relations_source ON task_relations(source_task_id);

-- Task/asset provenance supplements the legacy assets.task_ids association. The
-- array remains the compatibility source for existing query and cleanup paths;
-- this relation records how each association was obtained for operator review.
CREATE TABLE IF NOT EXISTS task_asset_links (
    task_id        BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    asset_id       BIGINT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    source         TEXT NOT NULL DEFAULT 'system',
    source_summary TEXT NOT NULL DEFAULT '',
    source_node_id BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    tested         BOOLEAN NOT NULL DEFAULT false,
    tested_at      TIMESTAMPTZ,
    tested_by      TEXT,
    approval_state TEXT NOT NULL DEFAULT 'approved',
    approved_at    TIMESTAMPTZ,
    approved_by    TEXT,
    approval_reason TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, asset_id)
);
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS tested BOOLEAN;
UPDATE task_asset_links SET tested=false WHERE tested IS NULL;
ALTER TABLE task_asset_links ALTER COLUMN tested SET DEFAULT false;
ALTER TABLE task_asset_links ALTER COLUMN tested SET NOT NULL;
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS tested_at TIMESTAMPTZ;
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS tested_by TEXT;
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS approval_state TEXT;
UPDATE task_asset_links SET approval_state='approved' WHERE approval_state IS NULL OR approval_state='';
ALTER TABLE task_asset_links ALTER COLUMN approval_state SET DEFAULT 'approved';
ALTER TABLE task_asset_links ALTER COLUMN approval_state SET NOT NULL;
ALTER TABLE task_asset_links DROP CONSTRAINT IF EXISTS task_asset_links_approval_state_check;
ALTER TABLE task_asset_links ADD CONSTRAINT task_asset_links_approval_state_check CHECK (approval_state IN ('approved','pending','revoked','blocked'));
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS approved_by TEXT;
ALTER TABLE task_asset_links ADD COLUMN IF NOT EXISTS approval_reason TEXT;
UPDATE task_asset_links SET approval_reason='' WHERE approval_reason IS NULL;
ALTER TABLE task_asset_links ALTER COLUMN approval_reason SET DEFAULT '';
ALTER TABLE task_asset_links ALTER COLUMN approval_reason SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_task_asset_links_asset ON task_asset_links(asset_id, task_id);
CREATE INDEX IF NOT EXISTS idx_task_asset_links_tested ON task_asset_links(task_id, tested, asset_id);
CREATE INDEX IF NOT EXISTS idx_task_asset_links_node ON task_asset_links(source_node_id)
    WHERE source_node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_task_asset_links_approval ON task_asset_links(task_id, approval_state, asset_id);

-- Task-local deletion tombstones. These intentionally have no asset foreign key:
-- a globally shared asset may be deleted/recreated while the current task must
-- continue to reject the same normalized identity until an operator reattaches it.
CREATE TABLE IF NOT EXISTS task_asset_blocks (
    task_id    BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    asset_key  TEXT NOT NULL,
    asset_type TEXT NOT NULL,
    host_key   TEXT NOT NULL DEFAULT '',
    asset_id   BIGINT,
    reason     TEXT NOT NULL DEFAULT '',
    blocked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    blocked_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, asset_key)
);
ALTER TABLE task_asset_blocks ADD COLUMN IF NOT EXISTS block_kind TEXT NOT NULL DEFAULT 'deleted'
    CHECK (block_kind IN ('manual','deleted'));
CREATE INDEX IF NOT EXISTS idx_task_asset_blocks_host ON task_asset_blocks(task_id, host_key);
CREATE INDEX IF NOT EXISTS idx_task_asset_blocks_asset ON task_asset_blocks(asset_id);

-- Match Go's task-local host identity, including URL-only imported records and
-- canonical IPv6. CIDR and other free-form scope text are not coerced to hosts.
CREATE OR REPLACE FUNCTION task_asset_normalized_host(value TEXT)
RETURNS TEXT AS $$
DECLARE
    normalized TEXT := lower(btrim(COALESCE(value,'')));
    parsed INET;
BEGIN
    IF normalized ~ '^[a-z][a-z0-9+.-]*://' THEN
        normalized := regexp_replace(normalized, '^[a-z][a-z0-9+.-]*://', '');
        normalized := split_part(split_part(split_part(normalized, '/', 1), '?', 1), '#', 1);
        normalized := regexp_replace(normalized, '^.*@', '');
        IF left(normalized,1)='[' THEN
            normalized := split_part(substr(normalized,2), ']', 1);
        ELSE
            normalized := regexp_replace(normalized, ':[0-9]+$', '');
        END IF;
    END IF;
    normalized := btrim(normalized, '[]');
    parsed := try_inet(normalized);
    IF parsed IS NOT NULL AND position('/' IN normalized)=0 THEN
        RETURN host(parsed);
    END IF;
    RETURN regexp_replace(normalized, '\.$', '');
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE OR REPLACE FUNCTION task_asset_host(asset assets)
RETURNS TEXT AS $$
SELECT task_asset_normalized_host(COALESCE(NULLIF(btrim(asset.domain),''),
    NULLIF(btrim(asset.ip),''), CASE WHEN asset.url ~ '^[a-zA-Z][a-zA-Z0-9+.-]*://' THEN asset.url END,''));
$$ LANGUAGE SQL IMMUTABLE;

CREATE OR REPLACE FUNCTION task_asset_host_within(child_host TEXT, parent_host TEXT)
RETURNS BOOLEAN AS $$
WITH normalized AS (
    SELECT task_asset_normalized_host(child_host) AS child,
           task_asset_normalized_host(parent_host) AS parent
)
SELECT child<>'' AND parent<>'' AND
    (child=parent OR (try_inet(parent) IS NULL AND right(child,length(parent)+1)='.'||parent))
FROM normalized;
$$ LANGUAGE SQL IMMUTABLE;

-- URLs are normalized on insertion; paths, parameters and their case stay part
-- of the identity. Host-based tombstones additionally constrain derived rows.
CREATE OR REPLACE FUNCTION task_asset_identity_key(asset assets)
RETURNS TEXT AS $$
SELECT CASE asset.type
    WHEN 'root_domain' THEN 'root_domain:'||task_asset_normalized_host(asset.domain)
    WHEN 'subdomain' THEN 'subdomain:'||task_asset_normalized_host(asset.domain)
    WHEN 'ip' THEN 'ip:'||task_asset_normalized_host(asset.ip)
    WHEN 'service' THEN CASE WHEN COALESCE(asset.url,'')<>'' THEN 'service:'||asset.url
        ELSE 'service:'||task_asset_host(asset)||':'||COALESCE(asset.port,0)::text||':'||lower(btrim(COALESCE(asset.service_name,''))) END
    WHEN 'endpoint' THEN 'endpoint:'||COALESCE(asset.url,'')||':'||upper(btrim(COALESCE(asset.method,'')))
    WHEN 'app' THEN 'app:'||lower(btrim(COALESCE(NULLIF(asset.bundle_id,''),asset.app_name,'')))
    ELSE asset.type||':'||asset.id::text END;
$$ LANGUAGE SQL IMMUTABLE;

CREATE OR REPLACE FUNCTION task_asset_blocked(p_task_id BIGINT, p_asset_id BIGINT)
RETURNS BOOLEAN AS $$
SELECT EXISTS (
    SELECT 1 FROM task_asset_blocks block JOIN assets t ON t.id=p_asset_id
    WHERE block.task_id=p_task_id
      AND (block.asset_id=p_asset_id
           OR block.asset_key=task_asset_identity_key(t)
           OR (block.asset_type IN ('root_domain','subdomain','ip') AND block.host_key<>''
               AND task_asset_host_within(task_asset_host(t),block.host_key)))
);
$$ LANGUAGE SQL STABLE;

-- An owner's services/endpoints have no independent approval decision: all
-- matching parent hosts must be approved, and a missing parent fails closed.
CREATE OR REPLACE FUNCTION task_asset_owner_approval_state(p_task_id BIGINT, p_asset_id BIGINT)
RETURNS TEXT AS $$
WITH target AS (
    SELECT a, l.approval_state FROM assets a
    JOIN task_asset_links l ON l.asset_id=a.id AND l.task_id=p_task_id
    WHERE a.id=p_asset_id
), parent_states AS (
    SELECT parent_link.approval_state,
           task_asset_blocked(p_task_id,parent.id) AS blocked
    FROM target t
    JOIN task_asset_links parent_link ON parent_link.task_id=p_task_id
    JOIN assets parent ON parent.id=parent_link.asset_id
    WHERE (t.a).type IN ('service','endpoint')
      AND parent.type IN ('root_domain','subdomain','ip')
      AND task_asset_host_within(task_asset_host(t.a),task_asset_host(parent))
)
SELECT CASE
    WHEN task_asset_blocked(p_task_id,p_asset_id) THEN 'blocked'
    ELSE COALESCE((SELECT CASE
        WHEN (t.a).type NOT IN ('service','endpoint') THEN t.approval_state
        WHEN EXISTS (SELECT 1 FROM parent_states WHERE blocked OR approval_state='blocked') THEN 'blocked'
        WHEN EXISTS (SELECT 1 FROM parent_states WHERE approval_state='revoked') THEN 'revoked'
        WHEN NOT EXISTS (SELECT 1 FROM parent_states)
          OR EXISTS (SELECT 1 FROM parent_states WHERE approval_state='pending') THEN 'pending'
        ELSE 'approved' END FROM target t), 'revoked') END;
$$ LANGUAGE SQL STABLE;

-- A local association overrides all inherited sources; otherwise any directly
-- related source may supply authorization. Current-task tombstones always win.
CREATE OR REPLACE FUNCTION task_asset_effective_approval_state(p_task_id BIGINT, p_asset_id BIGINT)
RETURNS TEXT AS $$
WITH auth_tasks AS (
    SELECT p_task_id AS task_id
    WHERE EXISTS (SELECT 1 FROM task_asset_links l WHERE l.task_id=p_task_id AND l.asset_id=p_asset_id)
    UNION ALL
    SELECT relation.source_task_id
    FROM task_relations relation
    WHERE relation.task_id=p_task_id
      AND NOT EXISTS (SELECT 1 FROM task_asset_links l WHERE l.task_id=p_task_id AND l.asset_id=p_asset_id)
), candidate_states AS (
    SELECT task_asset_owner_approval_state(auth.task_id,p_asset_id) AS state
    FROM auth_tasks auth
    JOIN task_asset_links own ON own.task_id=auth.task_id AND own.asset_id=p_asset_id
)
SELECT CASE
  WHEN task_asset_blocked(p_task_id,p_asset_id) THEN 'blocked'
  ELSE COALESCE((
    SELECT state FROM candidate_states
    ORDER BY CASE state WHEN 'approved' THEN 0 WHEN 'pending' THEN 1 WHEN 'revoked' THEN 2 ELSE 3 END
    LIMIT 1
  ), 'revoked')
END;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION task_asset_effectively_approved(p_task_id BIGINT, p_asset_id BIGINT)
RETURNS BOOLEAN AS $$
SELECT task_asset_effective_approval_state(p_task_id,p_asset_id)='approved';
$$ LANGUAGE SQL STABLE;

-- Shared by startup and archive restore. Save historic operator revocations
-- before neutralizing child decisions. Unknown revocations are conservatively
-- retained, while repeated runs cannot recreate a tombstone after reattachment.
CREATE OR REPLACE FUNCTION normalize_derived_task_asset_approvals(p_task_id BIGINT DEFAULT NULL)
RETURNS VOID AS $$
BEGIN
    INSERT INTO task_asset_blocks(task_id,asset_key,asset_type,host_key,asset_id,reason,blocked_at,blocked_by)
    SELECT l.task_id,task_asset_identity_key(a),a.type,task_asset_host(a),a.id,
           COALESCE(NULLIF(l.approval_reason,''),'历史服务/接口撤回授权'),
           l.updated_at,COALESCE(l.approved_by,'')
    FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
    WHERE a.type IN ('service','endpoint') AND l.approval_state='revoked'
      AND COALESCE(l.approval_reason,'')<>'继承父资产撤回' AND COALESCE(l.approved_by,'')<>'inherited'
      AND (p_task_id IS NULL OR l.task_id=p_task_id)
    ON CONFLICT (task_id,asset_key) DO NOTHING;

    UPDATE task_asset_links l SET approval_state='approved',approved_at=NULL,approved_by=NULL,
        approval_reason='继承父资产授权'
    FROM assets a WHERE a.id=l.asset_id AND a.type IN ('service','endpoint')
      AND (p_task_id IS NULL OR l.task_id=p_task_id)
      AND (l.approval_state<>'approved' OR l.approved_at IS NOT NULL OR l.approved_by IS NOT NULL
           OR COALESCE(l.approval_reason,'')<>'继承父资产授权');
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_task_asset_links_upd ON task_asset_links;
CREATE TRIGGER trg_task_asset_links_upd BEFORE UPDATE ON task_asset_links
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Keep provenance rows synchronized when existing asset upsert paths append or
-- remove task ids. Detailed callers overwrite the generic source after upsert.
CREATE OR REPLACE FUNCTION sync_task_asset_links() RETURNS trigger AS $$
DECLARE
    agent_discovery boolean := COALESCE(current_setting('artex.agent_discovery', true), '') = 'on';
BEGIN
    INSERT INTO task_asset_links(
        task_id, asset_id, source, source_summary, approval_state, approval_reason
    )
    SELECT task.id,
           NEW.id,
           CASE WHEN agent_discovery THEN 'agent' ELSE 'system' END,
           CASE WHEN agent_discovery THEN 'Agent 通过 insert_assets 登记' ELSE '任务执行期间自动关联' END,
           CASE WHEN agent_discovery AND NEW.type NOT IN ('service','endpoint') THEN 'pending' ELSE 'approved' END,
           CASE WHEN NEW.type IN ('service','endpoint') THEN '继承父资产授权'
                WHEN agent_discovery THEN 'Agent 发现，等待用户审批' ELSE '' END
    FROM unnest(NEW.task_ids) AS requested(task_id)
    JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
    ON CONFLICT (task_id, asset_id) DO NOTHING;

    DELETE FROM task_asset_links link
    WHERE link.asset_id=NEW.id
      AND NOT (link.task_id=ANY(NEW.task_ids));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_assets_task_links ON assets;
CREATE TRIGGER trg_assets_task_links AFTER INSERT OR UPDATE OF task_ids ON assets
    FOR EACH ROW EXECUTE FUNCTION sync_task_asset_links();

-- Existing installations receive an auditable legacy source without rewriting
-- task_ids. Ignore stale array ids that no longer resolve to a live task.
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT task.id, asset.id, 'legacy', '由历史任务资产关联迁移'
FROM assets asset
CROSS JOIN LATERAL unnest(asset.task_ids) AS requested(task_id)
JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO NOTHING;

-- New approval fields are conservative for legacy associations: existing links
-- were explicitly usable before approval was introduced.
UPDATE task_asset_links SET approval_state='approved'
WHERE approval_state IS NULL OR approval_state='';

SELECT normalize_derived_task_asset_approvals();

-- Backfill the task-local tested flag for existing installations. Facts and
-- findings already anchored to an asset are durable evidence that an agent
-- tested it; later writes maintain the flag directly from the write tools.
WITH first_test AS (
    SELECT task.id AS task_id, anchor.asset_id,
           MIN(node.created_at) AS tested_at,
           (ARRAY_AGG(NULLIF(node.origin,'') ORDER BY node.created_at, node.id)
             FILTER (WHERE NULLIF(node.origin,'') IS NOT NULL))[1] AS tested_by
    FROM tasks task
    JOIN exploration_nodes node ON node.exploration_id=task.exploration_id
                               AND node.kind IN ('fact','finding')
                               AND node.state <> 'origin'
    JOIN exploration_anchors anchor ON anchor.node_id=node.id
    GROUP BY task.id, anchor.asset_id
)
UPDATE task_asset_links link
SET tested=true,
    tested_at=COALESCE(link.tested_at, first_test.tested_at),
    tested_by=COALESCE(NULLIF(link.tested_by,''), first_test.tested_by)
FROM first_test
WHERE link.task_id=first_test.task_id
  AND link.asset_id=first_test.asset_id
  AND NOT link.tested;

-- Ordered task-level LLM failover chain. A quota-exhausted entry is skipped
-- until the user saves/resets the chain, which clears all failure state.
CREATE TABLE IF NOT EXISTS task_llm_profiles (
    task_id          BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    profile_id       BIGINT NOT NULL REFERENCES llm_profiles(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL CHECK (position >= 0),
    status           TEXT NOT NULL DEFAULT 'ready'
                       CHECK (status IN ('ready','quota_exhausted')),
    last_error       TEXT,
    exhausted_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, profile_id),
    UNIQUE (task_id, position)
);
CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_order ON task_llm_profiles(task_id, position);
CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_profile ON task_llm_profiles(profile_id, task_id);
CREATE INDEX IF NOT EXISTS idx_tasks_llm_profile ON tasks(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_active_llm_profile ON tasks(active_llm_profile_id) WHERE active_llm_profile_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_task_llm_profiles_upd ON task_llm_profiles;
CREATE TRIGGER trg_task_llm_profiles_upd BEFORE UPDATE ON task_llm_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One-time-compatible backfill: old pinned tasks become one-entry chains. A user
-- can still clear the chain later because the update path also clears the legacy
-- llm_profile_id column, preventing this block from re-adding it on restart.
INSERT INTO task_llm_profiles(task_id, profile_id, position)
SELECT t.id, t.llm_profile_id, 0
FROM tasks t
WHERE t.llm_profile_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id)
ON CONFLICT DO NOTHING;
UPDATE tasks t
SET active_llm_profile_id = t.llm_profile_id
WHERE t.active_llm_profile_id IS NULL
  AND t.llm_profile_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id AND x.profile_id=t.llm_profile_id);

-- 任务测试范围（资产覆盖度的分母 + 授权边界）。
--   自动填(source='auto')：insertAssets 顶层按 worker 显式插入的资产类型加保守范围
--     （root_domain→root_domain，subdomain/service/endpoint→subdomain(host)，ip→ip）；
--     side-effect 派生的资产不入范围（钩子在 handler 顶层，派生在 db 层内部）。
--   agent 填(source='agent')：add_task_scope 加 company/root_domain/subdomain/ip。
-- 覆盖度 = 匹配 active 行的 assets（分母）中，被 fact 节点锚定过的占比（分子）。
CREATE TABLE IF NOT EXISTS task_scope (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('company','root_domain','subdomain','ip','cidr','icp','keyword')),
    company_id  BIGINT REFERENCES companies(id) ON DELETE CASCADE,  -- kind='company'
    domain      TEXT,          -- root_domain / subdomain
    net         CIDR,          -- ip / cidr
    value       TEXT,          -- icp / keyword
    source      TEXT NOT NULL DEFAULT 'auto' CHECK (source IN ('auto','agent','manual')),
    reason      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 旧库升级：扩展任务范围，使其与企业范围的单文本框识别能力一致。
ALTER TABLE task_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE task_scope DROP CONSTRAINT IF EXISTS task_scope_kind_check;
ALTER TABLE task_scope ADD CONSTRAINT task_scope_kind_check
    CHECK (kind IN ('company','root_domain','subdomain','ip','cidr','icp','keyword'));
-- 去重：同一 task 的同一条范围只存一次（自动填批量插入靠它幂等）。
DROP INDEX IF EXISTS uq_task_scope;
DROP INDEX IF EXISTS uq_task_scope_v2;
CREATE UNIQUE INDEX IF NOT EXISTS uq_task_scope_v3 ON task_scope(
    task_id, kind, COALESCE(domain,''), COALESCE(net::text,''), COALESCE(company_id,0), md5(COALESCE(value,'')));
CREATE INDEX IF NOT EXISTS idx_ts_domain  ON task_scope(domain) WHERE kind IN ('root_domain','subdomain');
CREATE INDEX IF NOT EXISTS idx_ts_net     ON task_scope USING GIST(net inet_ops) WHERE kind IN ('ip','cidr');
CREATE INDEX IF NOT EXISTS idx_ts_company ON task_scope(company_id) WHERE kind = 'company';

-- =====================================================================
-- E. Agents / 提示词模板 / 变量目录
-- =====================================================================
CREATE TABLE IF NOT EXISTS agents (
    id                BIGSERIAL PRIMARY KEY,
    key               TEXT NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_]*$'),
    name              TEXT NOT NULL,
    description       TEXT,
    role              TEXT NOT NULL,
    builtin           BOOLEAN NOT NULL DEFAULT true,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    llm_profile_id    BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    current_prompt_id BIGINT,
    max_turns         INTEGER NOT NULL DEFAULT 0,
    run_seconds       INTEGER NOT NULL DEFAULT 1200,
    web_search        BOOLEAN NOT NULL DEFAULT false,
    interactive_shell BOOLEAN NOT NULL DEFAULT false,
    wrapup_prompt     TEXT NOT NULL DEFAULT '',
    wrapup_max_turns  INTEGER NOT NULL DEFAULT 0,
    task_timeout_wrapup_prompt    TEXT NOT NULL DEFAULT '',
    task_timeout_wrapup_max_turns INTEGER NOT NULL DEFAULT 0,
    trigger_run_mode     TEXT    NOT NULL DEFAULT 'serial'  CHECK (trigger_run_mode IN ('serial','parallel')),
    trigger_merge_mode   TEXT    NOT NULL DEFAULT 'all' CHECK (trigger_merge_mode IN ('by_task','all','none')),
    trigger_max_parallel INTEGER NOT NULL DEFAULT 5,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agents_role_ck CHECK (role IN ('goals','main','planner','worker','assistant'))
);
-- 加列迁移(已发版,旧库升级补列;新库 CREATE 已含。迁移不带 CHECK:旧库存量安全 + 后端写入白名单兜底)。
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_run_mode     TEXT    NOT NULL DEFAULT 'serial';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_merge_mode   TEXT    NOT NULL DEFAULT 'all';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_max_parallel INTEGER NOT NULL DEFAULT 5;
-- per-agent LLM 绑定(agent 级默认模型):列自初版即在上方 CREATE 中,此 ALTER 仅为极旧库兜底(幂等)。
ALTER TABLE agents ADD COLUMN IF NOT EXISTS llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
-- run_seconds 单次 run 墙钟默认 600→1200:只改列默认(影响将来新插入的行),不动旧库存量行。
ALTER TABLE agents ALTER COLUMN run_seconds SET DEFAULT 1200;
CREATE INDEX IF NOT EXISTS idx_agents_llm_profile ON agents(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
DROP TRIGGER IF EXISTS trg_agents_upd ON agents;
CREATE TRIGGER trg_agents_upd BEFORE UPDATE ON agents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS agent_prompts (
    id            BIGSERIAL PRIMARY KEY,
    agent_id      BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    version       INT NOT NULL,
    template_text TEXT NOT NULL,
    note          TEXT,
    updated_by    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, version)
);
-- 循环外键：agents.current_prompt_id → agent_prompts.id（需在两表创建后加）
DO $$ BEGIN
    ALTER TABLE agents ADD CONSTRAINT fk_agents_curprompt
        FOREIGN KEY (current_prompt_id) REFERENCES agent_prompts(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS agent_prompt_vars (
    id          BIGSERIAL PRIMARY KEY,
    agent_id    BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    var_name    TEXT NOT NULL,
    description TEXT,
    example     TEXT,
    source      TEXT NOT NULL CHECK (source IN ('exploration','runtime','distilled')),
    UNIQUE (agent_id, var_name)
);

-- =====================================================================
-- F. MCP 服务
-- =====================================================================
CREATE TABLE IF NOT EXISTS mcp_servers (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    transport   TEXT NOT NULL CHECK (transport IN ('stdio','http')),
    command     TEXT,
    args        JSONB NOT NULL DEFAULT '[]',
    env         JSONB NOT NULL DEFAULT '{}',
    url         TEXT,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_mcp_upd ON mcp_servers;
CREATE TRIGGER trg_mcp_upd BEFORE UPDATE ON mcp_servers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 默认数据源占位：ScopeSentry 资产同步 MCP（地址与认证均留空、未启用）。
-- 供「资产同步」页检测数据源是否已配置；用户在页面填入 url 与 X-API-Key 后再启用。
-- 仅在缺失时插入，绝不覆盖用户已配置/已启用的服务器（schema.sql 每次启动都会 Exec）。
INSERT INTO mcp_servers (name, transport, url, env, enabled)
VALUES ('ScopeSentry', 'http', NULL, '{"X-API-Key":""}', false)
ON CONFLICT (name) DO NOTHING;

CREATE TABLE IF NOT EXISTS mcp_tools_cache (
    id            BIGSERIAL PRIMARY KEY,
    server_id     BIGINT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    tool_name     TEXT NOT NULL,
    description   TEXT,
    schema        JSONB,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, tool_name)
);

-- =====================================================================
-- G. 可见性：agent × mcp / skill
-- =====================================================================
CREATE TABLE IF NOT EXISTS agent_visibility (
    agent_id      BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    resource_kind TEXT   NOT NULL CHECK (resource_kind IN ('mcp')),
    resource_id   BIGINT NOT NULL,
    mcp_tool_name TEXT   NOT NULL DEFAULT '',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, resource_kind, resource_id, mcp_tool_name)
);
CREATE INDEX IF NOT EXISTS idx_vis_resource ON agent_visibility(resource_kind, resource_id);

CREATE TABLE IF NOT EXISTS agent_skill_visibility (
    agent_id   BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_name TEXT   NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, skill_name)
);
CREATE INDEX IF NOT EXISTS idx_askv_skill ON agent_skill_visibility(skill_name);

-- Skill 调用账本（见 db/skill_usage.go）。一次 Skill() 调用一行，只记维度不记正文。
-- 刻意不设外键：任务/会话删除后统计仍要保留（与 llm_usage 同理），skill 本身也只是
-- 文件系统上的目录名，没有对应的表。
CREATE TABLE IF NOT EXISTS skill_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    skill          TEXT NOT NULL,
    agent_key      TEXT,
    task_id        BIGINT,
    exploration_id BIGINT,
    intent_id      BIGINT,
    session_id     TEXT,
    args_len       INTEGER NOT NULL DEFAULT 0,
    -- false = 模型点名了一个不存在的 skill(未命中)。这类行同样保留：它反映"想用但没有"
    -- 的缺口，是补 skill 的依据。
    found          BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_skill_usage_skill ON skill_usage(skill, ts DESC);
CREATE INDEX IF NOT EXISTS idx_skill_usage_task  ON skill_usage(task_id);

-- 工具调用账本（见 db/tool_usage.go）。一次实际 CoreTool.Call 一行，只记归属维度，
-- 不保存工具参数或返回内容。刻意不设外键，任务、会话或自定义工具删除后仍保留统计。
CREATE TABLE IF NOT EXISTS tool_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    tool_key       TEXT NOT NULL,
    agent_key      TEXT,
    task_id        BIGINT,
    exploration_id BIGINT,
    intent_id      BIGINT,
    session_id     TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_usage_tool ON tool_usage(tool_key, ts DESC);
CREATE INDEX IF NOT EXISTS idx_tool_usage_task ON tool_usage(task_id);

-- MCP tool calls use a dedicated ledger instead of parsing the dynamic
-- mcp__server__tool runtime name. No foreign keys by design: task/session or MCP
-- deletion must not erase historical usage statistics.
CREATE TABLE IF NOT EXISTS mcp_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_id      BIGINT,
    server_name    TEXT NOT NULL,
    tool_name      TEXT NOT NULL,
    agent_key      TEXT,
    task_id        BIGINT,
    exploration_id BIGINT,
    intent_id      BIGINT,
    session_id     TEXT
);
CREATE INDEX IF NOT EXISTS idx_mcp_usage_server ON mcp_usage(server_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_usage_tool ON mcp_usage(server_id, tool_name, ts DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_usage_task ON mcp_usage(task_id);

-- =====================================================================
-- H. 内置工具目录
-- =====================================================================
CREATE TABLE IF NOT EXISTS tools (
    key         TEXT PRIMARY KEY,
    system      BOOLEAN NOT NULL DEFAULT true,
    description TEXT    NOT NULL DEFAULT '',
    schema      JSONB   NOT NULL DEFAULT '{}',
    agents      JSONB   NOT NULL DEFAULT '[]',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    kind        TEXT    NOT NULL DEFAULT 'builtin',
    exec        JSONB   NOT NULL DEFAULT '{}',
    deferred    BOOLEAN NOT NULL DEFAULT false,
    directory   TEXT    NOT NULL DEFAULT '',
    usage_help  TEXT    NOT NULL DEFAULT '',
    when_to_use TEXT    NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE tools ADD COLUMN IF NOT EXISTS directory   TEXT NOT NULL DEFAULT '';
ALTER TABLE tools ADD COLUMN IF NOT EXISTS usage_help  TEXT NOT NULL DEFAULT '';
ALTER TABLE tools ADD COLUMN IF NOT EXISTS when_to_use TEXT NOT NULL DEFAULT '';
DROP TRIGGER IF EXISTS trg_tools_upd ON tools;
CREATE TRIGGER trg_tools_upd BEFORE UPDATE ON tools
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- =====================================================================
-- I. 会话（对话页）
-- =====================================================================
CREATE TABLE IF NOT EXISTS conversations (
    id             BIGSERIAL PRIMARY KEY,
    agent_key      TEXT NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL,
    pinned_at      TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_conversations_llm_profile ON conversations(llm_profile_id) WHERE llm_profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations(pinned_at DESC) WHERE pinned_at IS NOT NULL;
DROP TRIGGER IF EXISTS trg_conversations_upd ON conversations;
CREATE TRIGGER trg_conversations_upd BEFORE UPDATE ON conversations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS conversation_activities (
    id                 BIGSERIAL PRIMARY KEY,
    conversation_id    BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    worker             TEXT,
    kind               TEXT,
    tool               TEXT,
    tool_use_id        TEXT,
    is_error           BOOLEAN NOT NULL DEFAULT false,
    summary            TEXT,
    detail             TEXT,
    input_tokens       INTEGER,
    output_tokens      INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_conv_act ON conversation_activities(conversation_id, id);

-- =====================================================================
-- J. Agent 触发器
-- =====================================================================
CREATE TABLE IF NOT EXISTS agent_triggers (
    id                          BIGSERIAL PRIMARY KEY,
    agent_key                   TEXT NOT NULL,
    enabled                     BOOLEAN NOT NULL DEFAULT true,
    interval_sec                INTEGER NOT NULL DEFAULT 0,
    on_finding                  BOOLEAN NOT NULL DEFAULT false,
    on_goal_met                 BOOLEAN NOT NULL DEFAULT false,
    on_task_timeout             BOOLEAN NOT NULL DEFAULT false,
    on_tool_call                BOOLEAN NOT NULL DEFAULT false,
    on_task_create              BOOLEAN NOT NULL DEFAULT false,
    interval_message            TEXT NOT NULL DEFAULT '',
    finding_message             TEXT NOT NULL DEFAULT '',
    goal_message                TEXT NOT NULL DEFAULT '',
    task_timeout_message        TEXT NOT NULL DEFAULT '',
    tool_call_message           TEXT NOT NULL DEFAULT '',
    task_create_message         TEXT NOT NULL DEFAULT '',
    tool_names                  TEXT NOT NULL DEFAULT '',
    last_fire                   TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_triggers_agent ON agent_triggers(agent_key);
-- 加列迁移(已发版,旧库升级补列;新库 CREATE 已含这些列,ALTER 为 no-op)。幂等,每次启动可重复执行。
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_tool_call        BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_call_message   TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_names          TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_task_create      BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS task_create_message TEXT    NOT NULL DEFAULT '';
DROP TRIGGER IF EXISTS trg_agent_triggers_upd ON agent_triggers;
CREATE TRIGGER trg_agent_triggers_upd BEFORE UPDATE ON agent_triggers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS scheduler_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- =====================================================================
-- K. 拦截规则
-- =====================================================================
CREATE TABLE IF NOT EXISTS intercept_rules (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    priority        INTEGER NOT NULL DEFAULT 0,
    match_target    TEXT NOT NULL CHECK (match_target IN ('tool_name', 'tool_input')),
    match_type      TEXT NOT NULL CHECK (match_type IN ('string', 'regex')),
    pattern         TEXT NOT NULL,
    action          TEXT NOT NULL CHECK (action IN ('allow', 'deny', 'ask')),
    message         TEXT NOT NULL DEFAULT '',
    timeout_enabled BOOLEAN NOT NULL DEFAULT true,
    timeout_seconds INTEGER NOT NULL DEFAULT 60,
    timeout_action  TEXT    NOT NULL DEFAULT 'deny',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
DROP TRIGGER IF EXISTS trg_intercept_rules_upd ON intercept_rules;
CREATE TRIGGER trg_intercept_rules_upd BEFORE UPDATE ON intercept_rules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS intercept_pending (
    id              BIGSERIAL PRIMARY KEY,
    rule_id         BIGINT REFERENCES intercept_rules(id) ON DELETE SET NULL,
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
    task_id         TEXT,
    agent_name      TEXT NOT NULL DEFAULT '',
    tool_name       TEXT NOT NULL,
    tool_input      JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'allowed', 'denied', 'timeout')),
    -- 判定理由:规则命中时为规则 message;LLM 兜底判定时为模型给的简短理由(前缀 [模型])。
    reason          TEXT NOT NULL DEFAULT '',
    decided_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_intercept_pending_status ON intercept_pending(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_intercept_pending_task   ON intercept_pending(task_id, created_at DESC);
-- 补旧库:reason 列(已发版,加列要带 IF NOT EXISTS)。
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';

-- =====================================================================
-- L. 漏洞发现持久化
-- =====================================================================
CREATE TABLE IF NOT EXISTS findings (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT REFERENCES tasks(id) ON DELETE SET NULL,
    node_id     BIGINT REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    vulnclass   TEXT NOT NULL DEFAULT '',
    -- 漏洞名称(可读标题)；为空时前端回退展示 vulnclass。severity 取值：
    -- critical 严重 / high 高 / medium 中 / low 低（不加 CHECK，与 status 一致由 server 白名单校验）。
    name        TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    evidence    TEXT NOT NULL DEFAULT '',
    worker      TEXT NOT NULL DEFAULT '',
    asset_ids   JSONB NOT NULL DEFAULT '[]',
    -- 处置状态：pending 待处理 / in_progress 处理中 / confirmed 已确认 / resolved 已处理 /
    -- false_positive 误报 / ignored 忽略 / duplicate 重复 / risk_accepted 风险接受。
    -- 取值不加 CHECK：旧库靠下面的 ALTER 补列,CHECK 无法回填,统一由 server 侧白名单校验。
    status      TEXT NOT NULL DEFAULT 'pending',
    -- 漏洞详细报告(Markdown)；默认空,仅详情页读取/展示,不进列表接口以免 payload 膨胀。
    report      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE findings ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS name   TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS report TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_findings_task ON findings(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_findings_time ON findings(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_findings_status ON findings(status, created_at DESC);
-- 「按资产」视图靠 asset_ids @> '[<id>]' 反查发现,没有这个 GIN 索引就是全表扫。
CREATE INDEX IF NOT EXISTS idx_findings_asset_ids ON findings USING GIN(asset_ids jsonb_path_ops);

-- =====================================================================
-- M. 后端日志持久化
-- =====================================================================
CREATE TABLE IF NOT EXISTS server_logs (
    id         BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    level      TEXT NOT NULL DEFAULT 'info',
    tag        TEXT NOT NULL DEFAULT '',
    text       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_server_logs_id ON server_logs(id DESC);
