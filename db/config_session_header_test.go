package db

import "testing"

// TestProfileSessionHeaderKeyRoundTrip covers all SaveProfile statements. An
// empty header key is represented by the column's NOT NULL sentinel (""), not
// SQL NULL; otherwise saving a profile with the optional field blank fails.
func TestProfileSessionHeaderKeyRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	id, err := d.SaveProfile(&LLMProfile{
		Name: "t-session-header", Format: "openai", Model: "m", APIKey: "key-one",
	})
	if err != nil {
		t.Fatalf("create with empty session header: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	assertSessionHeaderKey(t, d, id, "")

	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID after create: profile=%v err=%v", p, err)
	}
	p.APIKey = "key-two"
	p.SessionHeaderKey = "x-session-id"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatalf("update key and session header: %v", err)
	}
	assertSessionHeaderKey(t, d, id, "x-session-id")

	p, err = d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("ProfileByID after keyed update: profile=%v err=%v", p, err)
	}
	p.APIKey = "" // keep the stored key and exercise the other UPDATE statement
	p.SessionHeaderKey = "   "
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatalf("clear session header: %v", err)
	}
	assertSessionHeaderKey(t, d, id, "")

	after, err := d.ProfileByID(id)
	if err != nil || after == nil {
		t.Fatalf("ProfileByID after clear: profile=%v err=%v", after, err)
	}
	if after.APIKey != "key-two" {
		t.Fatalf("blank API key on update replaced stored key: got %q", after.APIKey)
	}
}

func assertSessionHeaderKey(t *testing.T, d *DB, id int64, want string) {
	t.Helper()
	var got string
	if err := d.QueryRow(`SELECT session_header_key FROM llm_profiles WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatalf("read session_header_key: %v", err)
	}
	if got != want {
		t.Fatalf("session_header_key=%q, want %q", got, want)
	}
}
