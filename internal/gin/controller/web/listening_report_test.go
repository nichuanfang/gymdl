package web

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestValidReportTableRejectsInjection(t *testing.T) {
	for _, bad := range []string{"", "music; drop table x", "music-scrobbles", "1music"} {
		if validReportTable(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if !validReportTable("music_scrobble_records") {
		t.Fatal("valid table name rejected")
	}
}

// TestLoadListeningReportFromPostgres runs only when a read-only test DSN is
// provided; the normal suite must stay offline.
func TestLoadListeningReportFromPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GYMDL_REPORT_TEST_DSN"))
	if dsn == "" {
		t.Skip("set GYMDL_REPORT_TEST_DSN to run the Postgres report check")
	}
	report, err := loadListeningReport(context.Background(), dsn, "music_scrobble_records", "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Available || report.Scrobbles == 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if len(report.TopArtists) == 0 || len(report.Trend) == 0 {
		t.Fatalf("aggregates missing: %#v", report)
	}
}
