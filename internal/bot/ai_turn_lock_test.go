package bot

import (
	"sync"
	"testing"
	"time"
)

// TestAITurnLockSerialisesSameChat pins the fix for two quick messages running
// overlapping LLM turns and both writing the same history rows.
func TestAITurnLockSerialisesSameChat(t *testing.T) {
	app := &BotApp{aiTurnLocks: make(map[int64]*sync.Mutex)}
	var inFlight, maxInFlight int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := app.aiTurnLock(7)
			defer unlock()
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("%d turns ran at once for one chat", maxInFlight)
	}
	app.aiTurnMu.Lock()
	leaked := len(app.aiTurnLocks)
	app.aiTurnMu.Unlock()
	if leaked != 0 {
		t.Fatalf("lock map leaked %d entries", leaked)
	}
}

// TestAITurnLockAllowsDifferentChats keeps the lock per-chat: one slow user must
// not block another user's conversation.
func TestAITurnLockAllowsDifferentChats(t *testing.T) {
	app := &BotApp{aiTurnLocks: make(map[int64]*sync.Mutex)}
	first := app.aiTurnLock(1)
	defer first()
	done := make(chan struct{})
	go func() {
		unlock := app.aiTurnLock(2)
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a second chat waited on the first chat's lock")
	}
}
