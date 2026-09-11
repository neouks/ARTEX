package db

import (
	"reflect"
	"testing"
)

func TestBatchLineageKeepsRootsSeparateAndTerminatesCycles(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("batch lineage", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	store := d.Exploration(exp)
	var assets []int64
	for _, host := range []string{"batch-lineage-a.test", "batch-lineage-b.test"} {
		id, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: host})
		if err != nil {
			t.Fatal(err)
		}
		defer d.Exec(`DELETE FROM assets WHERE id=$1`, id)
		assets = append(assets, id)
	}
	first, err := store.AddNode(KindIntent, map[string]any{}, 1, "open", "test", assets[:1])
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.AddNode(KindIntent, map[string]any{}, 1, "open", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.AddNode(KindIntent, map[string]any{}, 1, "open", "test", assets[1:])
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]int64{{first, child}, {child, first}} {
		if err := store.Link(edge[0], RelDerivedFrom, edge[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.LineageAnchorAssetIDsForNodes([]int64{first, child, other, other})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first, child} {
		if !reflect.DeepEqual(got[id], assets[:1]) {
			t.Fatalf("root %d: %v", id, got[id])
		}
	}
	if !reflect.DeepEqual(got[other], assets[1:]) {
		t.Fatalf("unrelated root contaminated: %v", got[other])
	}
}
