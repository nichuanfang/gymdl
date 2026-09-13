package utils

import (
	"encoding/json"
	"testing"
)

func TestCDNResponseSupportsCurrentAndLegacyRefreshTime(t *testing.T) {
	var current CDNResponse
	if err := json.Unmarshal([]byte(`{"data":{"refresh_time":120}}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.Data.RefreshTimeSnake != 120 {
		t.Fatalf("current refresh time = %d", current.Data.RefreshTimeSnake)
	}

	var legacy CDNResponse
	if err := json.Unmarshal([]byte(`{"data":{"refreshTime":90}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Data.RefreshTime != 90 {
		t.Fatalf("legacy refresh time = %d", legacy.Data.RefreshTime)
	}
}
