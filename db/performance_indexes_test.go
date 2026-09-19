package db

import (
	"strings"
	"testing"
)

func TestSearchIndexesMatchQueryExpressions(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Tiny fixtures normally favour a sequential scan. Disable it here to check
	// index eligibility, not to claim a production timing or force runtime plans.
	if _, err = tx.Exec(`SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ table, expr, index string }{
		{"assets", mentionAssetText, "idx_mentions_assets_trgm"},
		{"findings", mentionFindingText, "idx_mentions_findings_trgm"},
		{"companies", mentionCompanyText, "idx_mentions_companies_trgm"},
		{"exploration_nodes", `(payload::text || ' ' || COALESCE(origin,''))`, "idx_broadcast_text_trgm"},
	} {
		rows, err := tx.Query(`EXPLAIN SELECT id FROM ` + tc.table + ` WHERE ` + tc.expr + ` ILIKE '%performance-test%'`)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan += line + "\n"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, tc.index) {
			t.Fatalf("index not eligible: %s", plan)
		}
	}
}
