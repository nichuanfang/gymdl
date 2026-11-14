package bot

import (
	"sync"
	"testing"
)

func TestMusicMaintenanceTasksAreMutuallyExclusive(t *testing.T) {
	app := &BotApp{}
	const workersPerTask = 20
	attempts := make(chan bool, workersPerTask*2)
	release := make(chan struct{})
	var workers sync.WaitGroup
	winnerCount := 0
	var winnerMu sync.Mutex

	for i := 0; i < workersPerTask; i++ {
		workers.Add(2)
		go func(chatID int64) {
			defer workers.Done()
			if !app.beginAIPlaylistRun(chatID) {
				attempts <- false
				return
			}
			attempts <- true
			winnerMu.Lock()
			winnerCount++
			winnerMu.Unlock()
			<-release
			app.finishAIPlaylistRun(chatID)
		}(int64(i + 1))
		go func(chatID int64) {
			defer workers.Done()
			if !app.beginEmptyTrashRun(chatID) {
				attempts <- false
				return
			}
			attempts <- true
			winnerMu.Lock()
			winnerCount++
			winnerMu.Unlock()
			<-release
			app.finishEmptyTrashRun(chatID)
		}(int64(i + workersPerTask + 1))
	}

	for i := 0; i < workersPerTask*2; i++ {
		<-attempts
	}
	winnerMu.Lock()
	gotWinners := winnerCount
	winnerMu.Unlock()
	if gotWinners != 1 {
		close(release)
		workers.Wait()
		t.Fatalf("concurrent AI playlist and empty trash starts = %d, want exactly one", gotWinners)
	}
	close(release)
	workers.Wait()

	if !app.beginEmptyTrashRun(100) {
		t.Fatal("a new maintenance task should start after the previous task finishes")
	}
	if !app.hasPendingEmptyTrash(100) {
		t.Fatal("empty-trash preflight should reserve its chat against new maintenance sessions")
	}
	app.finishEmptyTrashRun(100)
	if app.hasPendingEmptyTrash(100) {
		t.Fatal("finished empty-trash preflight must release its chat reservation")
	}

	if !app.beginPlaylistAssignmentRun(101) {
		t.Fatal("playlist assignment should start after the previous task finishes")
	}
	if app.beginAIPlaylistRun(102) || app.beginEmptyTrashRun(103) {
		t.Fatal("AI playlist and empty trash must wait for an active playlist assignment")
	}
	app.finishPlaylistAssignmentRun(101)
	if !app.beginAIPlaylistRun(104) {
		t.Fatal("AI playlist should start after playlist assignment finishes")
	}
	app.finishAIPlaylistRun(104)
}
