package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/bot"
	"github.com/nichuanfang/gymdl/utils"
)

// WatchManager 监听管理器
type WatchManager struct {
	watchers   map[string]*fsnotify.Watcher
	mu         sync.Mutex
	eventCh    chan fsnotify.Event
	stopCh     chan struct{}
	debounceMu sync.Mutex
	eventMap   map[string]time.Time
	wg         sync.WaitGroup
	cfg        *config.Config
}

// 创建监听管理器
func NewWatchManager(c *config.Config) *WatchManager {
	return &WatchManager{
		watchers: make(map[string]*fsnotify.Watcher),
		eventCh:  make(chan fsnotify.Event, 2048),
		stopCh:   make(chan struct{}),
		eventMap: make(map[string]time.Time),
		cfg:      c,
	}
}

// 递归添加目录及其所有子目录
func (wm *WatchManager) AddDirRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return wm.AddDir(path)
		}
		return nil
	})
}

// 添加单个目录监听
func (wm *WatchManager) AddDir(dir string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if _, ok := wm.watchers[dir]; ok {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(dir); err != nil {
		return err
	}
	wm.watchers[dir] = watcher

	wm.wg.Add(1)
	go wm.watchLoop(watcher, dir)
	return nil
}

// 去抖动逻辑：同一路径事件1秒内只处理一次
func (wm *WatchManager) debounce(event fsnotify.Event) bool {
	wm.debounceMu.Lock()
	defer wm.debounceMu.Unlock()
	now := time.Now()
	last, ok := wm.eventMap[event.Name]
	if ok && now.Sub(last) < time.Second {
		return false
	}
	wm.eventMap[event.Name] = now
	return true
}

// 监听协程
func (wm *WatchManager) watchLoop(watcher *fsnotify.Watcher, dir string) {
	defer wm.wg.Done()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !wm.debounce(event) {
				continue
			}
			select {
			case wm.eventCh <- event:
			case <-wm.stopCh:
				return
			}

			if event.Op&fsnotify.Create == fsnotify.Create {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					wm.AddDirRecursive(event.Name)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			utils.ErrorWithFormat("watcher error:", err)
		case <-wm.stopCh:
			return
		}
	}
}

// 停止所有监听和worker
func (wm *WatchManager) Stop() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	close(wm.stopCh)
	for _, watcher := range wm.watchers {
		watcher.Close()
	}
	close(wm.eventCh)
	wm.watchers = nil // 清空map即可，无需重新make
	wm.wg.Wait()
}

// 文件大小稳定性检测，interval为检测间隔，checks为检测次数
func isFileStable(path string, interval time.Duration, checks int) bool {
	var lastSize int64 = -1
	for i := 0; i < checks; i++ {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return false
		}
		size := info.Size()
		if lastSize != -1 && size != lastSize {
			lastSize = size
			time.Sleep(interval)
			continue
		}
		lastSize = size
		time.Sleep(interval)
	}
	return true
}

// SendTelegram 发送telegram消息
func SendTelegram(msg string) {
	notifier := bot.GetNotifier()
	if notifier != nil {
		notifier.Send(msg)
	} else {
		utils.WarnWithFormat("telegram未初始化,消息发送失败")
	}
}

// 启动worker池处理事件
func (wm *WatchManager) StartWorkerPool(workerCount int) {
	for i := 0; i < workerCount; i++ {
		wm.wg.Add(1)
		go func(id int) {
			defer wm.wg.Done()
			for event := range wm.eventCh {
				info, err := os.Stat(event.Name)
				if err != nil {
					continue
				}
				if event.Op&(fsnotify.Create|fsnotify.Write) != 0 && !info.IsDir() {
					if isFileStable(event.Name, 1*time.Second, 2) {
						utils.DebugWithFormat("[Monitor] Worker %d: Music file ready: %s", id, event.Name)
						err := HandleEvent(event.Name, wm.cfg)
						if err != nil {
							continue
						}
						SendTelegram(fmt.Sprintf("🎉入库成功: 【%s】 ", filepath.Base(event.Name)))
					} else {
						utils.DebugWithFormat("[Monitor] Worker %d: File not stable yet: %s", id, event.Name)
					}
				}
			}
		}(i)
	}
}
