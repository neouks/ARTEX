package server

import "fmt"

// SeedTool only inserts missing rows. Backfill the upstream d531ea9 Worker
// capability once on old catalogs; preserve enabled flags and all other roles.
// Later user unbinding remains authoritative on subsequent starts.
func (s *Server) seedWorkerTraceBindings() error {
	const flag = "worker_targeted_trace_bindings_v1"
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, flag); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM settings WHERE key=$1 AND value='true')`, flag).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM tools WHERE system=true AND key IN ('search_all_worker_traces','get_worker_trace')`).Scan(&count); err != nil {
		return err
	}
	if count != 2 {
		return fmt.Errorf("跨 work 回看工具尚未完成注册，下次启动重试")
	}
	if _, err := tx.Exec(`UPDATE tools SET agents=agents || '"worker"'::jsonb
		WHERE system=true AND key IN ('search_all_worker_traces','get_worker_trace') AND NOT (agents ? 'worker')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, flag); err != nil {
		return err
	}
	return tx.Commit()
}
