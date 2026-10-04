package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCombinedSearchFilteredPageFiltersBeforeGlobalPagination(t *testing.T) {
	providers := map[string][]SearchResultItem{
		"netease": {
			{Platform: "netease", SongID: "skip", Name: "unrelated", IsVIP: true},
			{Platform: "netease", SongID: "n1", Name: "Needle one", IsVIP: true},
			{Platform: "netease", SongID: "n2", Artists: "NEEDLE artist", IsVIP: true},
			{Platform: "netease", SongID: "n3", Album: "needle album", IsVIP: true},
			{Platform: "netease", SongID: "n4", URL: "https://example.test/needle", IsVIP: true},
		},
		"qq": {
			{Platform: "qq", SongID: "q0", Name: "needle qq zero", IsVIP: true},
			{Platform: "qq", SongID: "q1", Name: "needle qq free", IsVIP: false},
			{Platform: "qq", SongID: "q2", Name: "needle qq two", IsVIP: true},
		},
	}
	search := func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		items := providers[platform]
		if offset >= len(items) {
			return nil, nil
		}
		end := min(offset+limit, len(items))
		return append([]SearchResultItem(nil), items[offset:end]...), nil
	}
	filters := searchResultFilters{query: "NEEDLE", vip: "vip"}

	first, total, hasMore, truncated, errs := combinedSearchPageFilteredContext(context.Background(), []string{"netease", "qq"}, "query", filters, 0, 3, search)
	if len(errs) != 0 || truncated || total != 3 || !hasMore {
		t.Fatalf("unexpected first filtered page metadata: total=%d hasMore=%v truncated=%v errors=%v", total, hasMore, truncated, errs)
	}
	if got, want := searchItemKeys(first), []string{"netease:n1", "qq:q0", "netease:n2"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("first filtered page = %v, want %v", got, want)
	}

	second, total, hasMore, truncated, errs := combinedSearchPageFilteredContext(context.Background(), []string{"netease", "qq"}, "query", filters, 3, 3, search)
	if len(errs) != 0 || truncated || total != 3 || hasMore {
		t.Fatalf("unexpected second filtered page metadata: total=%d hasMore=%v truncated=%v errors=%v", total, hasMore, truncated, errs)
	}
	if got, want := searchItemKeys(second), []string{"qq:q2", "netease:n3", "netease:n4"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("second filtered page = %v, want %v", got, want)
	}
	seen := make(map[string]bool)
	for _, item := range append(first, second...) {
		key := item.Platform + ":" + item.SongID
		if seen[key] {
			t.Fatalf("filtered pages overlap at %s", key)
		}
		seen[key] = true
	}
}

func TestCombinedSearchPlatformFilterOnlyQueriesSelectedProvider(t *testing.T) {
	var calls []string
	search := func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		calls = append(calls, platform)
		if offset > 0 {
			return nil, nil
		}
		return []SearchResultItem{{Platform: platform, SongID: platform, Name: "needle"}}, nil
	}
	items, _, _, truncated, errs := combinedSearchPageFilteredContext(context.Background(), []string{"netease", "qq"}, "query", searchResultFilters{platform: "qq", query: "needle"}, 0, 10, search)
	if len(errs) != 0 || truncated || fmt.Sprint(calls) != "[qq]" {
		t.Fatalf("platform filter queried wrong providers: calls=%v truncated=%v errors=%v", calls, truncated, errs)
	}
	if got, want := searchItemKeys(items), []string{"qq:qq"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("platform-filtered results = %v, want %v", got, want)
	}
}

func TestSearchFilteredPrefixReportsCandidateCapAndProviderExhaustion(t *testing.T) {
	filters := searchResultFilters{query: "not present"}
	fullPages := func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		items := make([]SearchResultItem, limit)
		for i := range items {
			items[i] = SearchResultItem{Platform: platform, SongID: fmt.Sprint(offset + i), Name: "different"}
		}
		return items, nil
	}
	items, truncated, err := searchFilteredPrefixWithPageSize(context.Background(), "netease", "query", 2, 5, 5, filters, fullPages, nil)
	if err != nil || len(items) != 0 || !truncated {
		t.Fatalf("candidate cap should report incomplete scan: items=%v truncated=%v err=%v", items, truncated, err)
	}

	shortPage := func(_ context.Context, platform, _ string, limit, _ int) ([]SearchResultItem, error) {
		return []SearchResultItem{{Platform: platform, SongID: "only", Name: "different"}}, nil
	}
	items, truncated, err = searchFilteredPrefixWithPageSize(context.Background(), "netease", "query", 2, 5, 5, filters, shortPage, nil)
	if err != nil || len(items) != 0 || truncated {
		t.Fatalf("short provider page should mark the source exhausted: items=%v truncated=%v err=%v", items, truncated, err)
	}

	shortPageBeyondCap := func(_ context.Context, platform, _ string, _ int, _ int) ([]SearchResultItem, error) {
		return []SearchResultItem{
			{Platform: platform, SongID: "one", Name: "different"},
			{Platform: platform, SongID: "two", Name: "different"},
			{Platform: platform, SongID: "three", Name: "different"},
		}, nil
	}
	items, truncated, err = searchFilteredPrefixWithPageSize(context.Background(), "netease", "query", 2, 2, 5, filters, shortPageBeyondCap, nil)
	if err != nil || len(items) != 0 || !truncated {
		t.Fatalf("unscanned items beyond the candidate cap should report truncation: items=%v truncated=%v err=%v", items, truncated, err)
	}
}

func TestSearchFilteredPrefixKeepsPartialMatchesOnProviderFailure(t *testing.T) {
	search := func(_ context.Context, _ string, _ string, limit, offset int) ([]SearchResultItem, error) {
		if offset >= 2 {
			return nil, errors.New("provider page failed")
		}
		return []SearchResultItem{
			{Platform: "netease", SongID: "match", Name: "needle"},
			{Platform: "netease", SongID: "skip", Name: "other"},
		}, nil
	}
	items, truncated, err := searchFilteredPrefixWithPageSize(context.Background(), "netease", "query", 3, 10, 2, searchResultFilters{query: "needle"}, search, nil)
	if err == nil || err.Error() != "provider page failed" || truncated {
		t.Fatalf("expected partial provider failure, truncated=%v err=%v", truncated, err)
	}
	if got, want := searchItemKeys(items), []string{"netease:match"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("partial filtered matches = %v, want %v", got, want)
	}
}

func TestParseSearchRequestParsesAndValidatesOptionalFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parse := func(rawQuery string) (searchRequest, string) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/search?"+rawQuery, nil)
		return parseSearchRequest(ctx)
	}

	request, message := parse("keyword=test&platform=netease,qq&filter_query=+Needle+&filter_platform=qq&filter_vip=VIP")
	if message != "" || request.filters.query != "Needle" || request.filters.platform != "qq" || request.filters.vip != "vip" {
		t.Fatalf("optional filters parsed incorrectly: request=%#v message=%q", request, message)
	}
	if _, message := parse("keyword=test&platform=netease&filter_platform=qq"); message == "" {
		t.Fatal("filter platform outside the selected source set should be rejected")
	}
	if _, message := parse("keyword=test&filter_vip=paid"); message == "" {
		t.Fatal("unsupported VIP filter should be rejected")
	}
	if _, message := parse("keyword=test&filter_query=" + strings.Repeat("x", 201)); message == "" {
		t.Fatal("overlong filter query should be rejected")
	}
}

func TestSearchJSONEndpointAppliesOptionalFiltersAndKeepsLegacyDefaults(t *testing.T) {
	previousSearch := searchProviderSearch
	t.Cleanup(func() { searchProviderSearch = previousSearch })
	searchProviderSearch = func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		if offset > 0 {
			return nil, nil
		}
		return []SearchResultItem{
			{Platform: platform, SongID: "skip", Name: "other", IsVIP: true},
			{Platform: platform, SongID: "match", Album: "The Needle Album", IsVIP: true},
			{Platform: platform, SongID: "free", Name: "needle free", IsVIP: false},
		}, nil
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/search?keyword=json-filter-test&platform=netease&filter_query=needle&filter_vip=vip", nil)
	HandleSearch(ctx)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"song_id":"match"`) || strings.Contains(body, `"song_id":"skip"`) || strings.Contains(body, `"song_id":"free"`) || !strings.Contains(body, `"truncated":false`) {
		t.Fatalf("JSON endpoint did not return filtered items and additive truncation field: status=%d body=%s", recorder.Code, body)
	}
}

func TestSearchStreamAppliesGlobalFiltersAndReportsTruncationField(t *testing.T) {
	previousSearch := searchProviderSearch
	t.Cleanup(func() { searchProviderSearch = previousSearch })
	searchProviderSearch = func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		if platform != "qq" {
			t.Errorf("platform filter should skip provider %q", platform)
			return nil, nil
		}
		if offset > 0 {
			return nil, nil
		}
		return []SearchResultItem{
			{Platform: platform, SongID: "skip", Name: "different", IsVIP: true},
			{Platform: platform, SongID: "match", Artists: "Needle artist", IsVIP: true},
			{Platform: platform, SongID: "free", Name: "needle free", IsVIP: false},
		}, nil
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/search/stream?keyword=sse-filter-test&platform=netease,qq&offset=0&limit=10&filter_query=needle&filter_platform=qq&filter_vip=vip", nil)
	HandleSearchStream(ctx)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"song_id":"match"`) || strings.Contains(body, `"song_id":"skip"`) || strings.Contains(body, `"song_id":"free"`) || !strings.Contains(body, `"truncated":false`) {
		t.Fatalf("SSE endpoint did not return filtered results and truncation status: status=%d body=%s", recorder.Code, body)
	}
}

func searchItemKeys(items []SearchResultItem) []string {
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.Platform + ":" + item.SongID
	}
	return keys
}

func TestSearchCompleteEventKeepsTotalAsPageCount(t *testing.T) {
	data, err := json.Marshal(searchCompleteEvent{Items: []SearchResultItem{{SongID: "one"}}, Total: 1, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"total":1`) || !strings.Contains(string(data), `"truncated":true`) {
		t.Fatalf("unexpected additive search completion fields: %s", data)
	}
}

func TestCombinedFilteredSearchReturnsEmptyWhenNoCandidatesMatch(t *testing.T) {
	search := func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		if offset > 0 {
			return nil, nil
		}
		return []SearchResultItem{{Platform: platform, SongID: "only", Name: "unrelated"}}, nil
	}
	items, total, hasMore, truncated, errs := combinedSearchPageFilteredContext(context.Background(), []string{"netease"}, "query", searchResultFilters{query: "missing"}, 0, 10, search)
	if len(items) != 0 || total != 0 || hasMore || truncated || len(errs) != 0 {
		t.Fatalf("unexpected empty filtered page: items=%v total=%d hasMore=%v truncated=%v errors=%v", items, total, hasMore, truncated, errs)
	}
}

func TestMatchesSearchResultFiltersSupportsVIPPlatformAndAllTextFields(t *testing.T) {
	item := SearchResultItem{
		Platform: "qq",
		Name:     "Song",
		Artists:  "Artist",
		Album:    "Hidden needle album",
		URL:      "https://example.test/song",
		IsVIP:    true,
	}
	if !matchesSearchResultFilters(item, searchResultFilters{query: "NEEDLE", platform: "qq", vip: "vip"}) {
		t.Fatal("case-insensitive album match with matching platform and VIP filter should pass")
	}
	if matchesSearchResultFilters(item, searchResultFilters{query: "artist", platform: "netease", vip: "vip"}) {
		t.Fatal("result from a different platform should not pass")
	}
	if matchesSearchResultFilters(item, searchResultFilters{vip: "free"}) {
		t.Fatal("VIP result should not pass the free-only filter")
	}
	item.IsVIP = false
	if !matchesSearchResultFilters(item, searchResultFilters{vip: "free"}) {
		t.Fatal("non-VIP result should pass the free-only filter")
	}
	if !matchesSearchResultFilters(item, searchResultFilters{query: "example.test/song"}) {
		t.Fatal("URL should be included in text filtering")
	}
}

func TestCombinedFilteredSearchPropagatesCandidateTruncation(t *testing.T) {
	search := func(_ context.Context, platform, _ string, limit, offset int) ([]SearchResultItem, error) {
		items := make([]SearchResultItem, limit)
		for i := range items {
			items[i] = SearchResultItem{Platform: platform, SongID: fmt.Sprint(offset + i), Name: "not a match"}
		}
		return items, nil
	}
	items, total, hasMore, truncated, errs := combinedSearchPageFilteredContext(context.Background(), []string{"netease"}, "query", searchResultFilters{query: "missing"}, 0, 10, search)
	if len(errs) != 0 || len(items) != 0 || total != 0 || hasMore || !truncated {
		t.Fatalf("candidate cap metadata not propagated: items=%v total=%d hasMore=%v truncated=%v errors=%v", items, total, hasMore, truncated, errs)
	}
}
