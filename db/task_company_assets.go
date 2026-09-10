package db

import (
	"database/sql"
	"net"
)

// Materialize explicitly supplied hosts without expanding CIDRs or guessing
// targets from ICP, certificate and other free-form enterprise scope evidence.
func seedTaskCompanyAssets(tx *sql.Tx, taskID int64, companyIDs []int64) error {
	rows, err := tx.Query(`SELECT c.name,s.kind,COALESCE(s.domain,''),COALESCE(s.net::text,''),COALESCE(s.value,'')
FROM company_scope s JOIN companies c ON c.id=s.company_id
WHERE s.company_id=ANY($1::bigint[]) ORDER BY s.id`, companyIDs)
	if err != nil {
		return err
	}
	type seed struct{ company, kind, domain, network, value string }
	var seeds []seed
	for rows.Next() {
		var v seed
		if err := rows.Scan(&v.company, &v.kind, &v.domain, &v.network, &v.value); err != nil {
			rows.Close()
			return err
		}
		seeds = append(seeds, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	assets := &AssetStore{tx: tx}
	for _, v := range seeds {
		var id int64
		switch v.kind {
		case "domain":
			id, err = assets.UpsertRootDomain(UpsertRootDomainReq{Domain: v.domain, TaskID: taskID})
		case "ip":
			ip, _, parseErr := net.ParseCIDR(v.network)
			if parseErr != nil {
				return parseErr
			}
			id, err = assets.UpsertIP(UpsertIPReq{IP: ip.String(), TaskID: taskID})
		case "keyword":
			host, _, protocol := parseURL(v.value)
			if host == "" || (protocol != "HTTP" && protocol != "HTTPS") {
				continue
			}
			id, err = assets.UpsertHTTPService(UpsertHTTPServiceReq{URL: v.value, TaskID: taskID})
		default:
			continue
		}
		if err != nil {
			return err
		}
		// Upserts also register URL parent hosts. Give newly created system links
		// durable user provenance so later Agent rediscovery cannot downgrade them.
		if _, err := tx.Exec(`UPDATE task_asset_links SET source='company',
source_summary=$3,approval_state='approved',approved_at=now(),approved_by='user',
approval_reason='用户提供：任务创建时关联企业'
WHERE task_id=$1 AND (asset_id=$2 OR source='system')`, taskID, id, "任务创建时关联企业："+v.company); err != nil {
			return err
		}
	}
	return nil
}
