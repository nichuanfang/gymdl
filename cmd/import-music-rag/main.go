package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/nichuanfang/gymdl/internal/musicdedupe"
)

func main() {
	inputPath := flag.String("input", "", "path to the JSON export of n8n music_rag rows")
	databasePath := flag.String("db", musicdedupe.DefaultDatabasePath, "path to Gymdl's shared SQLite database")
	flag.Parse()
	if *inputPath == "" {
		fmt.Fprintln(os.Stderr, "-input is required")
		os.Exit(2)
	}

	data, err := os.ReadFile(*inputPath)
	if err != nil {
		fatal("read export", err)
	}
	rows, err := musicdedupe.DecodeLegacyExport(data)
	if err != nil {
		fatal("decode export", err)
	}
	store, err := musicdedupe.Open(*databasePath)
	if err != nil {
		fatal("open SQLite database", err)
	}
	defer store.Close()

	stats, err := store.ImportMusicRAGOnce(context.Background(), rows)
	if err != nil {
		fatal("import music_rag rows", err)
	}
	if stats.AlreadyImported {
		fmt.Printf("music_rag import already completed; no rows changed (export rows: %d)\n", stats.Total)
		return
	}
	fmt.Printf("music_rag import complete: imported=%d skipped=%d total=%d\n", stats.Imported, stats.Skipped, stats.Total)
}

func fatal(prefix string, err error) {
	fmt.Fprintln(os.Stderr, prefix+":", err)
	os.Exit(1)
}
