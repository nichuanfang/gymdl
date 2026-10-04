package web

import (
	"fmt"
	"testing"
)

func TestCombinedSearchPageInterleavesPlatformsAndUsesGlobalOffsets(t *testing.T) {
	search := func(platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		items := make([]SearchResultItem, 0, limit)
		for i := offset; i < offset+limit; i++ {
			items = append(items, SearchResultItem{Platform: platform, SongID: fmt.Sprint(i), Name: platform})
		}
		return items, nil
	}
	platforms := []string{"netease", "qq", "youtube", "bilibili"}

	first, total, more, errs := combinedSearchPage(platforms, "query", 0, 10, search)
	if len(errs) != 0 || len(first) != 10 || !more || total != 10 {
		t.Fatalf("unexpected first page: len=%d total=%d more=%v errors=%v", len(first), total, more, errs)
	}
	wantFirst := []string{"netease:0", "qq:0", "youtube:0", "bilibili:0", "netease:1", "qq:1", "youtube:1", "bilibili:1", "netease:2", "qq:2"}
	for i, want := range wantFirst {
		if got := first[i].Platform + ":" + first[i].SongID; got != want {
			t.Fatalf("first page item %d = %q, want %q", i, got, want)
		}
	}

	second, secondTotal, secondMore, _ := combinedSearchPage(platforms, "query", 10, 10, search)
	if len(second) != 10 || secondTotal != 10 || !secondMore {
		t.Fatalf("unexpected second page: len=%d total=%d more=%v", len(second), secondTotal, secondMore)
	}
	seen := map[string]bool{}
	for _, item := range first {
		seen[item.Platform+":"+item.SongID] = true
	}
	for _, item := range second {
		key := item.Platform + ":" + item.SongID
		if seen[key] {
			t.Fatalf("global pages overlap at %s", key)
		}
	}
	if got := second[0].Platform + ":" + second[0].SongID; got != "youtube:2" {
		t.Fatalf("unexpected stable continuation: %q", got)
	}
}

func TestCombinedSearchPageEndBoundaryAndStableProviderOrder(t *testing.T) {
	search := func(platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		total := 12
		if platform == "qq" {
			total = 4
		}
		end := offset + limit
		if end > total {
			end = total
		}
		items := make([]SearchResultItem, 0, end-offset)
		for i := offset; i < end; i++ {
			items = append(items, SearchResultItem{Platform: platform, SongID: fmt.Sprint(i)})
		}
		return items, nil
	}
	page1, _, more, _ := combinedSearchPage([]string{"netease"}, "q", 0, 10, search)
	if len(page1) != 10 || !more {
		t.Fatalf("expected 10 results with lookahead: len=%d more=%v", len(page1), more)
	}
	page2, _, more, _ := combinedSearchPage([]string{"netease"}, "q", 10, 10, search)
	if len(page2) != 2 || more || page2[0].SongID != "10" || page2[1].SongID != "11" {
		t.Fatalf("unexpected terminal page: %#v more=%v", page2, more)
	}

	first, _, _, _ := combinedSearchPage([]string{"netease", "qq"}, "q", 0, 4, search)
	if got := []string{first[0].Platform, first[1].Platform, first[2].Platform, first[3].Platform}; fmt.Sprint(got) != "[netease qq netease qq]" {
		t.Fatalf("selected platform order is not stable: %v", got)
	}
}

func TestCombinedSearchPageSupportsHundredItemPages(t *testing.T) {
	search := func(platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		const available = 120
		end := offset + limit
		if end > available {
			end = available
		}
		items := make([]SearchResultItem, 0, end-offset)
		for i := offset; i < end; i++ {
			items = append(items, SearchResultItem{Platform: platform, SongID: fmt.Sprint(i)})
		}
		return items, nil
	}
	platforms := []string{"netease", "qq"}

	first, _, hasMore, errs := combinedSearchPage(platforms, "q", 0, 100, search)
	if len(errs) != 0 || len(first) != 100 || !hasMore {
		t.Fatalf("unexpected first 100-item page: len=%d hasMore=%v errs=%v", len(first), hasMore, errs)
	}
	second, _, hasMore, errs := combinedSearchPage(platforms, "q", 100, 100, search)
	if len(errs) != 0 || len(second) != 100 || !hasMore {
		t.Fatalf("unexpected second 100-item page: len=%d hasMore=%v errs=%v", len(second), hasMore, errs)
	}
	seen := make(map[string]bool, len(first))
	for _, item := range first {
		seen[item.Platform+":"+item.SongID] = true
	}
	for _, item := range second {
		if seen[item.Platform+":"+item.SongID] {
			t.Fatalf("100-item pages overlap at %s:%s", item.Platform, item.SongID)
		}
	}
	last, _, hasMore, errs := combinedSearchPage(platforms, "q", 200, 100, search)
	if len(errs) != 0 || len(last) != 40 || hasMore {
		t.Fatalf("unexpected terminal page after 200 results: len=%d hasMore=%v errs=%v", len(last), hasMore, errs)
	}
}
