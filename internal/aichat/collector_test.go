package aichat

import (
	"context"
	"sync"
	"testing"
)

func TestCollectorIsSafeUnderConcurrentToolCalls(t *testing.T) {
	collector := NewCollector()
	ctx := WithCollector(context.Background(), collector)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c := CollectorFrom(ctx)
			c.Add([]SearchResult{{Platform: "qq", SongID: itoa(n), Name: "song"}})
		}(i)
	}
	wg.Wait()
	if got := len(collector.Results()); got != 20 {
		t.Fatalf("collector lost updates: %d", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
