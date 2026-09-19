package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Immutable text expressions are shared by queries and their trigram indexes.
// concat_ws is STABLE in PostgreSQL and cannot be used in an expression index.
const mentionFindingText = `(COALESCE(name,'') || ' ' || COALESCE(vulnclass,'') || ' ' || COALESCE(summary,''))`
const mentionCompanyText = `(COALESCE(name,'') || ' ' || COALESCE(nkey,''))`
const mentionAssetText = `(COALESCE(domain,'') || ' ' || COALESCE(root_domain,'') || ' ' || COALESCE(ip,'') || ' ' || COALESCE(url,'') || ' ' || COALESCE(app_name,'') || ' ' || COALESCE(bundle_id,'') || ' ' || COALESCE(page_title,'') || ' ' || COALESCE(service_name,'') || ' ' || COALESCE(method,''))`

// Run outside a transaction while holding the startup advisory lock. Concurrent
// builds allow other application instances to keep reading/writing large tables.
func ensurePerformanceIndexes(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
		return fmt.Errorf("search extension: %w", err)
	}
	indexes := []struct{ name, definition string }{
		{"idx_mentions_findings_trgm", `ON findings USING gin (` + mentionFindingText + ` gin_trgm_ops)`},
		{"idx_mentions_companies_trgm", `ON companies USING gin (` + mentionCompanyText + ` gin_trgm_ops)`},
		{"idx_mentions_assets_trgm", `ON assets USING gin (` + mentionAssetText + ` gin_trgm_ops)`},
		{"idx_broadcast_text_trgm", `ON exploration_nodes USING gin ((payload::text || ' ' || COALESCE(origin,'')) gin_trgm_ops)`},
		{"idx_broadcast_exp_id", `ON exploration_nodes (exploration_id,id)`},
	}
	for _, idx := range indexes {
		var valid bool
		err := conn.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid=to_regclass($1)`, idx.name).Scan(&valid)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if valid {
			continue
		}
		if err == nil {
			if _, err = conn.ExecContext(ctx, `DROP INDEX CONCURRENTLY `+idx.name); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, `CREATE INDEX CONCURRENTLY `+idx.name+` `+idx.definition); err != nil {
			return fmt.Errorf("build %s: %w", idx.name, err)
		}
	}
	return nil
}
