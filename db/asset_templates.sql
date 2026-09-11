-- Old rows retain the conservative policy; callers explicitly choose the new default.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS asset_approval_template TEXT NOT NULL DEFAULT 'explicit_targets'
 CHECK(asset_approval_template IN ('all_assets','related_assets','explicit_targets'));

ALTER TABLE task_asset_blocks DROP CONSTRAINT IF EXISTS task_asset_blocks_block_kind_check;
ALTER TABLE task_asset_blocks ADD CONSTRAINT task_asset_blocks_block_kind_check CHECK(block_kind IN ('manual','deleted','invalid'));

CREATE OR REPLACE FUNCTION repair_task_asset_inputs(tid BIGINT DEFAULT NULL)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO task_asset_grants(task_id,kind,value,source,evidence)
 SELECT task_id,'cidr',net::text,'manual',COALESCE(reason,'') FROM task_scope
 WHERE source='manual' AND kind IN ('ip','cidr') AND net IS NOT NULL
 AND (tid IS NULL OR task_id=tid) ON CONFLICT DO NOTHING;
 INSERT INTO task_asset_blocks(task_id,asset_key,asset_type,host_key,asset_id,reason,blocked_by,block_kind)
 SELECT l.task_id,task_asset_identity_key(a),a.type,task_asset_host(a),a.id,
 '非法 Agent 主机名：请纠正资产，不可直接批准','system','invalid'
 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
 WHERE (tid IS NULL OR l.task_id=tid) AND l.source='agent'
 AND a.type IN ('root_domain','subdomain','service','endpoint')
 AND (task_asset_host(a) ~ '[_${}[:space:]]'
 OR (task_asset_host(a) NOT LIKE '%.%' AND try_inet(task_asset_host(a)) IS NULL
 AND NOT EXISTS(SELECT 1 FROM task_asset_grants g WHERE g.task_id=l.task_id AND g.kind='host' AND g.value=task_asset_host(a))))
 ON CONFLICT(task_id,asset_key) DO UPDATE SET
  asset_type=EXCLUDED.asset_type,
  host_key=EXCLUDED.host_key,
  asset_id=EXCLUDED.asset_id,
  reason=CASE WHEN task_asset_blocks.block_kind='invalid' THEN task_asset_blocks.reason
              ELSE concat_ws('；',NULLIF(task_asset_blocks.reason,''),EXCLUDED.reason) END,
  blocked_by=CASE WHEN COALESCE(task_asset_blocks.blocked_by,'')<>'' THEN task_asset_blocks.blocked_by ELSE EXCLUDED.blocked_by END,
  block_kind='invalid';
 UPDATE task_asset_links l SET approval_state='blocked' FROM task_asset_blocks b
 WHERE b.task_id=l.task_id AND b.asset_id=l.asset_id AND b.block_kind='invalid'
 AND (tid IS NULL OR l.task_id=tid) AND l.approval_state NOT IN ('blocked','revoked');
 UPDATE task_asset_links l SET approval_state='approved',approved_by='user',approved_at=COALESCE(approved_at,now()),
 approval_reason='修复用户提供资产审批'
 WHERE (tid IS NULL OR l.task_id=tid) AND source IN ('manual','direct','company','api','task')
 AND approval_state='pending' AND NOT task_asset_blocked(l.task_id,l.asset_id);
 -- Legacy task-seeded URLs carried user provenance only on the service row.
 -- Repair their exact host, not the synthetic ancestor root or discovered IPs.
 UPDATE task_asset_links parent_link SET approval_state='approved',approved_by='user',approved_at=now(),
 source='manual',source_summary='用户提供：历史服务的精确父主机',approval_reason='修复用户提供资产审批'
 FROM assets parent WHERE parent.id=parent_link.asset_id AND parent.type IN ('root_domain','subdomain','ip')
 AND parent_link.approval_state='pending' AND (tid IS NULL OR parent_link.task_id=tid)
 AND NOT task_asset_blocked(parent_link.task_id,parent.id)
 AND EXISTS(SELECT 1 FROM task_asset_links user_link JOIN assets supplied ON supplied.id=user_link.asset_id
  WHERE user_link.task_id=parent_link.task_id AND user_link.source IN ('manual','direct','company','api','task')
  AND user_link.approval_state='approved' AND NOT task_asset_blocked(user_link.task_id,supplied.id)
  AND task_asset_host(supplied)=task_asset_host(parent));
 INSERT INTO task_asset_grants(task_id,kind,value,root_domain,source,evidence)
 SELECT l.task_id,'host',task_asset_host(a),COALESCE(NULLIF(a.root_domain,''),CASE WHEN a.type='root_domain' THEN a.domain ELSE '' END),l.source,l.source_summary
 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
 WHERE (tid IS NULL OR l.task_id=tid) AND l.source IN ('manual','direct','company','api','task')
 AND l.approval_state='approved' AND task_asset_host(a)<>'' AND task_asset_host(a)!~'[_${}[:space:]]'
 ON CONFLICT DO NOTHING;
END $$;

-- Seeds are durable operator grants, never inferred from an Agent-supplied source.
CREATE TABLE IF NOT EXISTS task_asset_grants (
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('host','domain','cidr')),
 value TEXT NOT NULL,
 root_domain TEXT NOT NULL DEFAULT '',
 source TEXT NOT NULL,
 evidence TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(task_id,kind,value)
);

-- DNS record values on assets are global enrichment. Authorization evidence is
-- task-local so an A/AAAA observation made in one task cannot authorize an IP
-- in another task that happens to share the same global DNS asset row.
CREATE TABLE IF NOT EXISTS task_asset_dns_evidence (
 task_id BIGINT NOT NULL,
 dns_asset_id BIGINT NOT NULL,
 record_type TEXT NOT NULL CHECK(record_type IN ('A','AAAA')),
 ip INET NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(task_id,dns_asset_id,ip),
 FOREIGN KEY(task_id,dns_asset_id) REFERENCES task_asset_links(task_id,asset_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_task_asset_dns_evidence_ip ON task_asset_dns_evidence(task_id,ip);

CREATE OR REPLACE FUNCTION task_asset_template_allows(tid BIGINT, target assets)
RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((SELECT t.asset_approval_template='all_assets' OR EXISTS (
 SELECT 1 FROM task_asset_grants g WHERE g.task_id=tid AND (
  (g.kind='host' AND task_asset_host(target)=g.value)
  OR (g.kind='domain' AND task_asset_host_within(task_asset_host(target),g.value))
  OR (g.kind='cidr' AND try_inet(task_asset_host(target)) IS NOT NULL
      AND try_inet(task_asset_host(target)) <<= g.value::cidr)
  OR (t.asset_approval_template='related_assets' AND g.root_domain<>'' AND (
      task_asset_host_within(task_asset_host(target),g.root_domain)
      OR (target.type='ip' AND EXISTS (
         SELECT 1 FROM task_asset_dns_evidence evidence
         JOIN assets dns ON dns.id=evidence.dns_asset_id
         WHERE evidence.task_id=tid AND dns.type='subdomain'
          AND task_asset_host_within(dns.domain,g.root_domain)
          AND evidence.ip=try_inet(target.ip)
      ))
  ))
 )) FROM tasks t WHERE t.id=tid),false)
$$;

-- Finalize discovery inside its transaction, including side-effect roots/IPs.
CREATE OR REPLACE FUNCTION apply_task_asset_template() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a assets; sibling task_asset_links;
BEGIN
 SELECT * INTO a FROM assets WHERE id=NEW.asset_id;
 IF NEW.approval_state='pending' AND NOT task_asset_blocked(NEW.task_id,NEW.asset_id) THEN
  SELECT l.* INTO sibling FROM task_asset_links l JOIN assets other ON other.id=l.asset_id
   WHERE l.task_id=NEW.task_id AND l.asset_id<>NEW.asset_id
    AND a.type IN ('root_domain','subdomain','ip') AND other.type IN ('root_domain','subdomain','ip')
    AND task_asset_host(other)=task_asset_host(a)
    AND (l.approval_state IN ('revoked','blocked') OR (l.approval_state='approved' AND COALESCE(l.approved_by,'') NOT IN ('','inherited','template')))
   ORDER BY CASE l.approval_state WHEN 'blocked' THEN 0 WHEN 'revoked' THEN 1 ELSE 2 END LIMIT 1;
  IF FOUND THEN
   NEW.approval_state:=sibling.approval_state;
   NEW.approved_by:=sibling.approved_by; NEW.approved_at:=sibling.approved_at;
   NEW.approval_reason:=sibling.approval_reason;
  ELSIF task_asset_template_allows(NEW.task_id,a) THEN
   NEW.approval_state:='approved'; NEW.approved_at:=now(); NEW.approved_by:='template';
   NEW.approval_reason:='任务审批模板自动批准';
  END IF;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_apply_asset_template ON task_asset_links;
CREATE TRIGGER trg_apply_asset_template BEFORE INSERT OR UPDATE ON task_asset_links
 FOR EACH ROW EXECUTE FUNCTION apply_task_asset_template();

SELECT repair_task_asset_inputs(NULL);

-- Exact approved hosts suffice; unrelated synthetic pending ancestors do not
-- override them. Explicit ancestor revocations and blocks always win.
CREATE OR REPLACE FUNCTION task_asset_owner_approval_state(p_task_id BIGINT,p_asset_id BIGINT)
RETURNS TEXT LANGUAGE plpgsql STABLE AS $$
DECLARE
 target_host TEXT; target_type TEXT; target_state TEXT;
 parent_count BIGINT; denied BOOLEAN; revoked BOOLEAN;
 exact_pending BOOLEAN; exact_approved BOOLEAN; pending BOOLEAN;
BEGIN
 IF task_asset_blocked(p_task_id,p_asset_id) THEN RETURN 'blocked'; END IF;
 SELECT task_asset_host(a),a.type,l.approval_state INTO target_host,target_type,target_state
 FROM assets a JOIN task_asset_links l ON l.asset_id=a.id
 WHERE a.id=p_asset_id AND l.task_id=p_task_id;
 IF NOT FOUND THEN RETURN 'revoked'; END IF;
 -- Hosts are normalized on asset writes; only matching parents need their
 -- authorization/tombstone checked. Keep normalized parent reads together.
 WITH parents AS MATERIALIZED (
  SELECT p.id,task_asset_host(p) AS host,l.approval_state
  FROM task_asset_links l JOIN assets p ON p.id=l.asset_id
  WHERE l.task_id=p_task_id AND p.type IN ('root_domain','subdomain','ip')
 ), matching AS MATERIALIZED (
  SELECT * FROM parents WHERE host<>'' AND target_host<>'' AND
   (host=target_host OR (try_inet(host) IS NULL AND right(target_host,length(host)+1)='.'||host))
 )
 SELECT count(*),bool_or(approval_state='blocked' OR task_asset_blocked(p_task_id,id)),
  bool_or(approval_state='revoked'),bool_or(host=target_host AND approval_state='pending'),
  bool_or(host=target_host AND approval_state='approved'),bool_or(approval_state='pending')
 INTO parent_count,denied,revoked,exact_pending,exact_approved,pending FROM matching;
 IF denied THEN RETURN 'blocked'; END IF;
 IF revoked THEN RETURN 'revoked'; END IF;
 IF target_type NOT IN ('service','endpoint') THEN RETURN target_state; END IF;
 IF exact_pending THEN RETURN 'pending'; END IF;
 IF exact_approved THEN RETURN 'approved'; END IF;
 IF parent_count=0 OR pending THEN RETURN 'pending'; END IF;
 RETURN 'approved';
END
$$;
