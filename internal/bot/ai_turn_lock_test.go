package bot

import (
	"sync"
	"testing"
	"time"
)

// TestAITurnLockSerialisesSameChat pins the fix for two quick messages running
// overlapping LLM turns and both writing the same history rows.
func TestAITurnLockSerialisesSameChat(t *testing.T) {
	app := &BotApp{aiTurnLocks: make(map[int64]*aiTurnLockEntry)}
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
	app := &BotApp{aiTurnLocks: make(map[int64]*aiTurnLockEntry)}
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

func TestAITurnLockKeepsEntryWhileQueuedTurnsExist(t *testing.T) {
	app := &BotApp{aiTurnLocks: make(map[int64]*aiTurnLockEntry)}
	firstUnlock := app.aiTurnLock(9)
	secondAcquired := make(chan struct{})
	secondRelease := make(chan struct{})
	go func() {
		unlock := app.aiTurnLock(9)
		close(secondAcquired)
		<-secondRelease
		unlock()
	}()
	waitForAITurnLockRefs(t, app, 9, 2)
	firstUnlock()
	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("second turn did not acquire the chat lock")
	}

	thirdAcquired := make(chan struct{})
	thirdRelease := make(chan struct{})
	go func() {
		unlock := app.aiTurnLock(9)
		close(thirdAcquired)
		<-thirdRelease
		unlock()
	}()
	waitForAITurnLockRefs(t, app, 9, 2)
	app.aiTurnMu.Lock()
	entry := app.aiTurnLocks[9]
	app.aiTurnMu.Unlock()
	if entry == nil {
		t.Fatal("queued turns lost their shared lock entry")
	}
	close(secondRelease)
	select {
	case <-thirdAcquired:
	case <-time.After(time.Second):
		t.Fatal("third turn did not acquire the shared chat lock")
	}
	close(thirdRelease)
	waitForAITurnLockRefs(t, app, 9, 0)
	app.aiTurnMu.Lock()
	defer app.aiTurnMu.Unlock()
	if len(app.aiTurnLocks) != 0 {
		t.Fatalf("lock map leaked %d entries", len(app.aiTurnLocks))
	}
}

func waitForAITurnLockRefs(t *testing.T, app *BotApp, chatID int64, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.aiTurnMu.Lock()
		entry := app.aiTurnLocks[chatID]
		refs := 0
		if entry != nil {
			refs = entry.refs
		}
		app.aiTurnMu.Unlock()
		if refs == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lock references for chat %d did not become %d", chatID, want)
}
