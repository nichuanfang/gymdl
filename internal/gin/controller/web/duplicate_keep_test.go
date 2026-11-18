package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/internal/audiodupe"
)

func sampleDuplicateReport() audiodupe.Report {
	return audiodupe.Report{
		Status: "done",
		Groups: []audiodupe.Group{
			{Kind: audiodupe.KindSame, Tracks: []audiodupe.Track{{Path: "a/one.flac"}, {Path: "a/one.mp3"}}},
			{Kind: audiodupe.KindSimilar, Tracks: []audiodupe.Track{{Path: "b/live.flac"}, {Path: "b/studio.flac"}}},
		},
	}
}

func TestPlanDuplicateRemovalRejectsUnsafeChoices(t *testing.T) {
	report := sampleDuplicateReport()
	if _, err := planDuplicateRemoval(report, true, "a/one.flac", []string{"a/one.mp3"}); !errors.Is(err, errDuplicateScanRunning) {
		t.Fatal(err)
	}
	if _, err := planDuplicateRemoval(report, false, "a/one.flac", []string{"a/one.flac"}); err == nil || !strings.Contains(err.Error(), "不能同时删除") {
		t.Fatal(err)
	}
	if _, err := planDuplicateRemoval(report, false, "a/one.flac", []string{"b/live.flac"}); err == nil || !strings.Contains(err.Error(), "不在这一组") {
		t.Fatal(err)
	}
	if _, err := planDuplicateRemoval(report, false, "missing.flac", []string{"a/one.mp3"}); err == nil {
		t.Fatal("missing keep was accepted")
	}
	idle := report
	idle.Status = "idle"
	if _, err := planDuplicateRemoval(idle, false, "a/one.flac", []string{"a/one.mp3"}); err == nil {
		t.Fatal("idle report was accepted")
	}
	group, err := planDuplicateRemoval(report, false, "b/studio.flac", []string{"b/live.flac"})
	if err != nil || group.Kind != audiodupe.KindSimilar {
		t.Fatal(group, err)
	}
}

func TestDuplicateRemovalStopsBeforeTheFileWhenMetadataFails(t *testing.T) {
	var steps []string
	pruner := libraryPruner{
		lookup: func(context.Context, string) (string, error) {
			steps = append(steps, "lookup")
			return "song1", nil
		},
		dropVector: func(context.Context, string) error {
			steps = append(steps, "vector")
			return errors.New("pinecone down")
		},
		dropRows: func(context.Context, []string) error {
			steps = append(steps, "rows")
			return nil
		},
		dropFile: func(string) error {
			steps = append(steps, "file")
			return nil
		},
	}
	done, err := pruner.apply(context.Background(), []string{"a/one.mp3"})
	if err == nil || len(done) != 0 || strings.Join(steps, ",") != "lookup,vector" {
		t.Fatal(done, err, steps)
	}

	steps = nil
	pruner.dropVector = func(context.Context, string) error {
		steps = append(steps, "vector")
		return nil
	}
	pruner.dropRows = func(context.Context, []string) error {
		steps = append(steps, "rows")
		return errors.New("db down")
	}
	done, err = pruner.apply(context.Background(), []string{"a/one.mp3"})
	if err == nil || len(done) != 0 || strings.Join(steps, ",") != "lookup,vector,rows" {
		t.Fatal(done, err, steps)
	}
}

func TestDuplicateRemovalDeletesVectorRowsThenFile(t *testing.T) {
	var steps []string
	pruner := libraryPruner{
		lookup:     func(context.Context, string) (string, error) { return "song1", nil },
		dropVector: func(context.Context, string) error { steps = append(steps, "vector"); return nil },
		dropRows:   func(context.Context, []string) error { steps = append(steps, "rows"); return nil },
		dropFile:   func(string) error { steps = append(steps, "file"); return nil },
	}
	done, err := pruner.apply(context.Background(), []string{"a/one.mp3"})
	if err != nil || len(done) != 1 || strings.Join(steps, ",") != "vector,rows,file" {
		t.Fatal(done, err, steps)
	}
}

func TestNavidromePathMatchIsExactOrSuffix(t *testing.T) {
	if !navidromePathMatches("/music/a/one.flac", "a/one.flac") || navidromePathMatches("/music/a/one.flac.bak", "a/one.flac") {
		t.Fatal("path match drifted")
	}
	songs := parseNavidromeSongs([]byte(`[{"id":"song1","path":"/music/a/one.mp3"},{"id":"other","path":"/music/a/two.mp3"}]`))
	if len(songs) != 2 || songs[0].ID != "song1" {
		t.Fatal(songs)
	}
	id, err := chooseNavidromeSongID([]navidromeSongRef{
		{ID: "longer", Path: "/music/backup/a/one.flac"},
		{ID: "exact", Path: "a/one.flac"},
	}, "a/one.flac")
	if err != nil || id != "exact" {
		t.Fatal(id, err)
	}
	if _, err := chooseNavidromeSongID([]navidromeSongRef{
		{ID: "a", Path: "/music/a/one.flac"},
		{ID: "b", Path: "/other/a/one.flac"},
	}, "a/one.flac"); err == nil {
		t.Fatal("two suffix hits were accepted")
	}
}

func TestDropRemovedDuplicateTracksKeepsTheSurvivor(t *testing.T) {
	report := dropRemovedDuplicateTracks(sampleDuplicateReport(), []string{"a/one.mp3"})
	if len(report.Groups) != 1 || report.Groups[0].Kind != audiodupe.KindSimilar {
		t.Fatal(report.Groups)
	}
}
