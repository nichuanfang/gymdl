package music

import (
	"context"
	"errors"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/processor"
	"os/exec"
	"sync"
	"testing"
)

type workflowStub struct {
	download func() error
	steps    *[]string
}

func (p *workflowStub) Init(*config.Config)      {}
func (p *workflowStub) Name() processor.LinkType { return processor.LinkNetEase }
func (p *workflowStub) Songs() []*SongInfo {
	return []*SongInfo{{SongName: "Song", SongArtists: "Artist", Tidy: "WEBDAV_PENDING"}}
}
func (p *workflowStub) DownloadMusic(string, func(string)) error {
	if p.steps != nil {
		*p.steps = append(*p.steps, "download")
	}
	if p.download != nil {
		return p.download()
	}
	return nil
}
func (p *workflowStub) DownloadCommand(string) *exec.Cmd { return nil }
func (p *workflowStub) BeforeTidy() error {
	if p.steps != nil {
		*p.steps = append(*p.steps, "tag")
	}
	return nil
}
func (p *workflowStub) TidyMusic() error {
	if p.steps != nil {
		*p.steps = append(*p.steps, "tidy")
	}
	return nil
}
func (p *workflowStub) NeedRemoveDRM() bool     { return false }
func (p *workflowStub) DRMRemove() error        { return nil }
func (p *workflowStub) EncryptedExts() []string { return nil }
func (p *workflowStub) DecryptedExts() []string { return nil }
func TestWorkflowKeepsDedupeAndRecordInsideGate(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	recorded := false
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := RunWorkflowWithHooks(context.Background(), &workflowStub{download: func() error { close(entered); <-release; return nil }}, "first", nil, WorkflowHooks{After: func([]*SongInfo) error { recorded = true; return nil }})
		if err != nil {
			t.Error(err)
		}
	}()
	<-entered
	go func() {
		defer wg.Done()
		_, err := RunWorkflowWithHooks(context.Background(), &workflowStub{download: func() error { t.Error("duplicate was downloaded"); return nil }}, "second", nil, WorkflowHooks{Before: func() error {
			if !recorded {
				t.Error("record callback ran outside gate")
			}
			return errors.New("already owned")
		}})
		if err == nil {
			t.Error("preflight rejection ignored")
		}
	}()
	close(release)
	wg.Wait()
}
func TestWorkflowStagesFailuresAndCommittedResults(t *testing.T) {
	steps := []string{}
	songs, err := RunWorkflowWithHooks(context.Background(), &workflowStub{steps: &steps}, "song", nil, WorkflowHooks{After: func([]*SongInfo) error { return errors.New("disk full") }})
	if !errors.Is(err, ErrWorkflowCommitted) || len(songs) != 1 || songs[0].Tidy != "WEBDAV_PENDING" {
		t.Fatal("lost committed/deferred result", songs, err)
	}
	if len(steps) != 3 || steps[0] != "download" || steps[1] != "tag" || steps[2] != "tidy" {
		t.Fatal("pipeline order", steps)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = RunWorkflow(ctx, &workflowStub{}, "song", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled work executed")
	}
}

func TestBeforeStoreRejectionSkipsTidy(t *testing.T) {
	steps := []string{}
	_, err := RunWorkflowWithHooks(context.Background(), &workflowStub{steps: &steps}, "song", nil, WorkflowHooks{BeforeStore: func() error {
		return errors.New("听感很接近")
	}})
	if err == nil || err.Error() != "听感很接近" {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0] != "download" || steps[1] != "tag" {
		t.Fatal(steps)
	}
}
