package traffic

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestSyncTrafficFilteringAndReclaimLatency(t *testing.T) {
	tr, _ := openTraffic(t)
	// Deterministic fixture, only local temporary storage. Record request latency
	// includes waiting on the same write lock used by reclamation.
	for i := 0; i < 1200; i++ {
		tr.record(newFlow("sync.invalid", "GET", fmt.Sprintf("/api/order/%d", i), nil, []byte("同步证据 unique-response-content")))
	}
	q := PageQuery{Host: "sync.invalid", Body: "同步证据", Path: "/api/order", Status: "2xx", RespMin: -1, RespMax: -1, Sort: "resp_len", Order: "asc"}
	start := time.Now()
	a, total, err := tr.Page(q, 0, 20)
	if err != nil || total != 1200 || len(a) != 20 {
		t.Fatalf("page total=%d count=%d err=%v", total, len(a), err)
	}
	b, _, err := tr.Page(q, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range a {
		seen[row.ID] = true
	}
	for _, row := range b {
		if seen[row.ID] {
			t.Fatal("overlapping pages")
		}
	}
	t.Logf("1200-row FTS + path + status sorted pages: %s", time.Since(start))
	bulkRecord(tr, "purge.invalid", 100, 64*1024)
	start = time.Now()
	if _, err := tr.DeleteHost("purge.invalid"); err != nil {
		t.Fatal(err)
	}
	var latencies []time.Duration
	for i := 0; i < 100; i++ {
		s := time.Now()
		tr.record(newFlow("live.invalid", "GET", fmt.Sprintf("/live/%d", i), nil, []byte("live")))
		latencies = append(latencies, time.Since(s))
	}
	tr.reaping.Wait()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("reclaim and 100 recordings: %s, recording p95=%s max=%s", time.Since(start), latencies[94], latencies[99])
	rows, err := tr.Search("live.invalid", 0, 200)
	if err != nil || len(rows) != 100 {
		t.Fatalf("recordings lost: %d %v", len(rows), err)
	}
	start = time.Now()
	n, reclaimed, err := tr.DeleteAll()
	if err != nil || n != 1300 {
		t.Fatalf("clear %d %v", n, err)
	}
	t.Logf("clear %d rows: %s, reclaimed=%d bytes", n, time.Since(start), reclaimed)
	tr.record(newFlow("after.invalid", "GET", "/", nil, []byte("after")))
	if count, err := tr.Count(); err != nil || count != 1 {
		t.Fatalf("after clear: %d %v", count, err)
	}
}
