DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM tool_usage_migrations WHERE name='legacy_activity_v1') THEN
        RETURN;
    END IF;
    -- Coordinate the snapshot with live writes from another application instance.
    LOCK TABLE tool_usage_migrations, tool_usage, activity, conversation_activities IN SHARE ROW EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tool_usage_migrations WHERE name='legacy_activity_v1') THEN
        RETURN;
    END IF;

    WITH historical AS (
        SELECT a.id, a.created_at AS ts, a.tool AS tool_key,
               CASE WHEN a.worker LIKE 'work#%' THEN 'worker' ELSE a.worker END AS agent_key,
               (SELECT min(t.id) FROM tasks t WHERE t.exploration_id=a.exploration_id) AS task_id,
               a.exploration_id, a.node_id AS intent_id, NULL::text AS session_id,
               'exp-' || a.exploration_id AS scope
        FROM activity a
        WHERE a.kind='tool_use' AND COALESCE(a.tool,'')<>''
        UNION ALL
        SELECT a.id, a.created_at, a.tool, c.agent_key, NULL::bigint,
               NULL::bigint, NULL::bigint, 'conv-' || c.id, 'conv-' || c.id
        FROM conversation_activities a JOIN conversations c ON c.id=a.conversation_id
        WHERE a.kind='tool_use' AND COALESCE(a.tool,'')<>''
    ), metered AS (
        SELECT tool_key,
               CASE WHEN exploration_id IS NOT NULL THEN 'exp-' || exploration_id ELSE session_id END AS scope,
               count(*) AS n
        FROM tool_usage
        GROUP BY 1,2
    ), ranked AS (
        SELECT h.*,
               row_number() OVER (PARTITION BY h.scope,h.tool_key ORDER BY h.ts,h.id) AS rank,
               count(*) OVER (PARTITION BY h.scope,h.tool_key) - COALESCE(m.n,0) AS missing
        FROM historical h LEFT JOIN metered m ON m.scope=h.scope AND m.tool_key=h.tool_key
    )
    INSERT INTO tool_usage(ts,tool_key,agent_key,task_id,exploration_id,intent_id,session_id)
    SELECT ts,tool_key,agent_key,task_id,exploration_id,intent_id,session_id
    FROM ranked WHERE rank<=missing;

    INSERT INTO tool_usage_migrations(name) VALUES ('legacy_activity_v1');
END $$;
