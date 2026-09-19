package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChatMention is a lightweight search result. Details are read again on send.
type ChatMention struct {
	Kind        string `json:"kind"`
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type ChatMentionPage struct {
	Items      []ChatMention `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

var ErrInvalidChatMentionCursor = errors.New("分页位置无效，请重新搜索")

type chatMentionCursor struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Exact bool   `json:"exact"`
	Scope string `json:"scope"`
	Query string `json:"query"`
}

func ValidChatMentionKind(kind string) bool {
	switch kind {
	case "finding", "company", "asset", "endpoint", "ip", "app", "root_domain", "subdomain", "service":
		return true
	}
	return false
}

// SearchChatMentions searches the shared catalog, just like the asset/finding
// pages. Values remain SQL parameters; %, _ and backslash are literal search text.
func (d *DB) SearchChatMentions(ctx context.Context, kind, query string) ([]ChatMention, error) {
	page, err := d.SearchChatMentionsPage(ctx, kind, query, "")
	return page.Items, err
}

// SearchChatMentionsPage uses the last result's stable sort key rather than an
// offset, so loading later pages does not repeatedly skip all earlier results.
func (d *DB) SearchChatMentionsPage(ctx context.Context, kind, query, cursor string) (ChatMentionPage, error) {
	page := ChatMentionPage{Items: make([]ChatMention, 0)}
	if kind != "" && !ValidChatMentionKind(kind) {
		return page, fmt.Errorf("不支持的引用类型")
	}
	var after chatMentionCursor
	if cursor != "" {
		if len(cursor) > 2048 {
			return page, ErrInvalidChatMentionCursor
		}
		blob, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(blob, &after) != nil || after.ID <= 0 ||
			!ValidChatMentionKind(after.Kind) || after.Scope != kind || after.Query != query {
			return page, ErrInvalidChatMentionCursor
		}
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	// Exact IDs use the primary key; fuzzy candidates use the trigram index.
	// Keeping them in separate branches avoids an OR across incompatible indexes.
	exactID, parseErr := strconv.ParseInt(query, 10, 64)
	if parseErr != nil || exactID < 0 {
		exactID = 0
	}
	args := []any{pattern, after.ID, after.Kind, exactID}
	var branches []string
	add := func(table, k, label, description, search, scope string) {
		base := "SELECT " + k + " AS kind,id," + label + " AS label," + description + " AS description"
		if exactID > 0 && (cursor == "" || after.Exact) {
			where := "id=$4 AND " + scope
			if cursor != "" {
				where += " AND " + k + ">$3"
			}
			branches = append(branches, "("+base+",true AS exact FROM "+table+" WHERE "+where+")")
		}
		where := scope + " AND id<>$4 AND $1::text IS NOT NULL AND $2::bigint>=0 AND $3::text IS NOT NULL"
		if query != "" {
			where += " AND " + search + " ILIKE $1"
		}
		if cursor != "" && !after.Exact {
			where += " AND (id<$2 OR (id=$2 AND " + k + ">$3))"
		}
		branches = append(branches, "("+base+",false AS exact FROM "+table+" WHERE "+where+" ORDER BY id DESC LIMIT 21)")
	}
	if kind == "" || kind == "finding" {
		add("findings", "'finding'", "COALESCE(NULLIF(name,''),vulnclass)", "concat_ws(' · ',severity,status,left(summary,160))", mentionFindingText, "true")
	}
	if kind == "" || kind == "company" {
		add("companies", "'company'", "name", "nkey", mentionCompanyText, "true")
	}
	if kind != "finding" && kind != "company" {
		scope := "true"
		if kind != "" && kind != "asset" {
			args = append(args, kind)
			scope = "type=$5"
		}
		add("assets", "type", `CASE WHEN type='endpoint' THEN concat_ws(' ',NULLIF(method,''),url) ELSE COALESCE(NULLIF(app_name,''),NULLIF(url,''),NULLIF(domain,''),NULLIF(ip,''),NULLIF(bundle_id,''),'资产 #'||id::text) END`, `concat_ws(' · ',type,NULLIF(page_title,''),NULLIF(service_name,''),NULLIF(bundle_id,''),NULLIF(ip,''),port::text)`, mentionAssetText, scope)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := d.QueryContext(ctx, `SELECT kind,id,left(label,160),left(description,240) FROM (`+strings.Join(branches, " UNION ALL ")+`) matches ORDER BY exact DESC,id DESC,kind LIMIT 21`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ChatMention
		if err := rows.Scan(&item.Kind, &item.ID, &item.Label, &item.Description); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > 20 {
		page.Items = page.Items[:20]
		last := page.Items[19]
		blob, _ := json.Marshal(chatMentionCursor{last.ID, last.Kind, exactID > 0 && last.ID == exactID, kind, query})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(blob)
	}
	return page, nil
}
