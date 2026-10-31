package aichat

import "testing"

func TestHistoryDisabledWithoutDSN(t *testing.T) {
	source := HistorySource{}
	if source.Enabled() {
		t.Fatal("history without DSN must stay disabled")
	}
	profile, err := source.Load(t.Context())
	if err != nil || profile != nil {
		t.Fatalf("disabled history = (%#v,%v), want (nil,nil)", profile, err)
	}
}

func TestHistoryTableNameValidation(t *testing.T) {
	for _, name := range []string{"music_scrobble_records", "scrobbles_2"} {
		if !validTableName(name) {
			t.Fatalf("%q should be allowed", name)
		}
	}
	for _, name := range []string{"", "2bad", "music;drop", "music records", "schema.music"} {
		if validTableName(name) {
			t.Fatalf("%q should be rejected", name)
		}
	}
}
