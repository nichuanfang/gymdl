package task

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core/linkparser"
	"github.com/nichuanfang/gymdl/internal/storage"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/processor/music"
	"github.com/nichuanfang/gymdl/processor/video"
	"github.com/nichuanfang/gymdl/utils"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// Task 下载任务
type Task struct {
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	Platform  string            `json:"platform"`
	Status    TaskStatus        `json:"status"`
	Progress  string            `json:"progress"`
	Error     string            `json:"error,omitempty"`
	SongInfo  []*music.SongInfo `json:"song_info,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// TaskManager 任务管理器
type TaskManager struct {
	cfgMu       sync.RWMutex
	mu          sync.Mutex
	tasks       map[string]*Task
	order       []string
	sem         chan struct{}
	historyDB   *sql.DB
	historyErr  error
	cfg         *config.Config
	subscribers map[string]map[chan *Task]struct{}
	subsMu      sync.Mutex
}

// NewTaskManager 创建任务管理器
func NewTaskManager(cfg *config.Config) *TaskManager {
	return NewTaskManagerWithDBPath(cfg, storage.DefaultDatabasePath, "data/web_state/history.json")
}

// NewTaskManagerWithDBPath creates a task manager using an explicit SQLite
// path. legacyHistoryPath may be empty to disable one-time JSON import.
func NewTaskManagerWithDBPath(cfg *config.Config, dbPath, legacyHistoryPath string) *TaskManager {
	tm := &TaskManager{
		tasks:       make(map[string]*Task),
		order:       make([]string, 0),
		sem:         make(chan struct{}, 2),
		cfg:         cfg,
		subscribers: make(map[string]map[chan *Task]struct{}),
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		tm.historyErr = err
		utils.ErrorWithFormat("打开下载历史 SQLite 失败: %v", err)
		return tm
	}
	if err := initializeTaskHistory(db, ""); err != nil {
		_ = db.Close()
		tm.historyErr = err
		utils.ErrorWithFormat("初始化下载历史 SQLite 失败: %v", err)
		return tm
	}
	tm.historyDB = db
	if legacyHistoryPath != "" {
		if err := importLegacyTaskHistory(db, legacyHistoryPath); err != nil {
			utils.WarnWithFormat("导入旧下载历史失败，原 JSON 文件已保留: %v", err)
		}
	}
	return tm
}

func (tm *TaskManager) Close() error {
	if tm == nil {
		return nil
	}
	tm.mu.Lock()
	db := tm.historyDB
	tm.historyDB = nil
	tm.mu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

func (tm *TaskManager) InitializationError() error {
	if tm == nil {
		return fmt.Errorf("task manager is nil")
	}
	return tm.historyErr
}

// SubmitTask 提交下载任务
func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return &config.Config{}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		copy := *cfg
		return &copy
	}
	var snapshot config.Config
	if err := yaml.Unmarshal(data, &snapshot); err != nil {
		copy := *cfg
		return &copy
	}
	snapshot.ConfigFile = cfg.ConfigFile
	return &snapshot
}

func (tm *TaskManager) configSnapshot() *config.Config {
	tm.cfgMu.RLock()
	defer tm.cfgMu.RUnlock()
	return cloneConfig(tm.cfg)
}

// SetConfig applies a new immutable config snapshot to subsequently submitted tasks.
func (tm *TaskManager) SetConfig(cfg *config.Config) {
	tm.cfgMu.Lock()
	tm.cfg = cloneConfig(cfg)
	tm.cfgMu.Unlock()
}

func (tm *TaskManager) SubmitTask(rawURL string) (*Task, error) {
	if tm.historyErr != nil {
		return nil, fmt.Errorf("下载历史数据库不可用: %v", tm.historyErr)
	}
	if tm.historyDB == nil {
		return nil, fmt.Errorf("下载历史数据库已关闭")
	}
	cfg := tm.configSnapshot()
	link, executor := linkparser.ParseLinkWithConfig(cfg, rawURL)
	if link == "" {
		return nil, fmt.Errorf("暂不支持该类型的链接")
	}

	tm.mu.Lock()
	task := &Task{
		ID:        uuid.New().String(),
		URL:       link,
		Platform:  string(executor.Name()),
		Status:    TaskStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	tm.tasks[task.ID] = task
	tm.order = append(tm.order, task.ID)
	tm.mu.Unlock()

	go tm.runTask(task, executor, cfg)
	return task, nil
}

// runTask 执行下载任务
func (tm *TaskManager) runTask(task *Task, executor processor.Processor, cfg *config.Config) {
	// 获取并发槽位
	tm.sem <- struct{}{}
	defer func() { <-tm.sem }()

	// 防止处理器 panic 导致整个进程退出
	defer func() {
		if r := recover(); r != nil {
			tm.finishWithError(task.ID, fmt.Errorf("[%s] %s panic: %v", task.Platform, task.URL, r))
		}
	}()

	switch expr := executor.(type) {
	case music.Processor:
		expr.Init(cfg)
		tm.execMusicTask(task, expr)
	case video.Processor:
		expr.Init(cfg)
		tm.execVideoTask(task, expr)
	default:
		tm.updateTask(task.ID, func(t *Task) {
			t.Status = TaskStatusFailed
			t.Error = "未知处理器类型"
		})
	}
}

// execMusicTask 执行音乐下载任务
func (tm *TaskManager) execMusicTask(task *Task, p music.Processor) {
	tm.updateTask(task.ID, func(t *Task) { t.Status = TaskStatusRunning })

	err := p.DownloadMusic(task.URL, func(progress string) {
		tm.updateTask(task.ID, func(t *Task) { t.Progress = progress })
	})
	if err != nil {
		tm.finishWithError(task.ID, err)
		return
	}

	if err = p.BeforeTidy(); err != nil {
		tm.finishWithError(task.ID, err)
		return
	}

	if err = p.TidyMusic(); err != nil {
		tm.finishMusicTask(task.ID, p, err)
		return
	}
	tm.finishMusicTask(task.ID, p, nil)
}

// execVideoTask 执行视频下载任务
func (tm *TaskManager) execVideoTask(task *Task, p video.Processor) {
	tm.updateTask(task.ID, func(t *Task) { t.Status = TaskStatusRunning })

	if err := p.Download(task.URL); err != nil {
		tm.finishWithError(task.ID, err)
		return
	}
	tm.updateTask(task.ID, func(t *Task) { t.Status = TaskStatusCompleted })
}

// finishWithError 统一失败处理
func (tm *TaskManager) finishWithError(id string, err error) {
	tm.updateTask(id, func(t *Task) {
		t.Status = TaskStatusFailed
		t.Error = utils.TruncateString(err.Error(), 500)
	})
}

// finishMusicTask 音乐任务完成
func (tm *TaskManager) finishMusicTask(id string, p music.Processor, err error) {
	tm.updateTask(id, func(t *Task) {
		t.SongInfo = p.Songs()
		if err != nil {
			t.Status = TaskStatusFailed
			t.Error = utils.TruncateString(err.Error(), 500)
		} else {
			t.Status = TaskStatusCompleted
		}
	})
}

// updateTask 线程安全地更新任务并通知订阅者
func (tm *TaskManager) updateTask(id string, fn func(*Task)) {
	tm.mu.Lock()
	t, ok := tm.tasks[id]
	if !ok {
		tm.mu.Unlock()
		return
	}
	fn(t)
	t.UpdatedAt = time.Now()
	snapshot := *t
	tm.mu.Unlock()

	tm.notifySubscribers(&snapshot)

	if t.Status == TaskStatusCompleted || t.Status == TaskStatusFailed || t.Status == TaskStatusCancelled {
		tm.moveToHistory(id)
	}
}

// moveToHistory 移动到历史
func (tm *TaskManager) moveToHistory(id string) {
	if tm.historyDB == nil {
		utils.ErrorWithFormat("保存下载历史失败: SQLite 数据库不可用 (%v)", tm.historyErr)
		return
	}
	tm.mu.Lock()
	t, ok := tm.tasks[id]
	if !ok {
		tm.mu.Unlock()
		return
	}
	snapshot := *t
	tm.mu.Unlock()

	if err := insertTaskHistory(tm.historyDB, &snapshot); err != nil {
		utils.ErrorWithFormat("保存下载历史失败: %v", err)
		return
	}

	tm.mu.Lock()
	if _, ok := tm.tasks[id]; !ok {
		tm.mu.Unlock()
		return
	}
	delete(tm.tasks, id)
	for i, v := range tm.order {
		if v == id {
			tm.order = append(tm.order[:i], tm.order[i+1:]...)
			break
		}
	}
	tm.mu.Unlock()
}

// notifySubscribers 通知所有订阅者
func (tm *TaskManager) notifySubscribers(snapshot *Task) {
	tm.subsMu.Lock()
	subs := tm.subscribers[snapshot.ID]
	for ch := range subs {
		select {
		case ch <- snapshot:
		default:
		}
	}
	tm.subsMu.Unlock()
}

// GetActiveTasks 获取活跃任务
func (tm *TaskManager) GetActiveTasks() []*Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	result := make([]*Task, 0, len(tm.order))
	for _, id := range tm.order {
		if t, ok := tm.tasks[id]; ok {
			snapshot := *t
			result = append(result, &snapshot)
		}
	}
	return result
}

// HistoryFilter describes server-side filters applied before pagination.
type HistoryFilter struct {
	Query    string
	Platform string
	Status   string
	From     time.Time
	To       time.Time
}

// GetHistory preserves the original unfiltered history API.
func (tm *TaskManager) GetHistory(offset, limit int) ([]*Task, int) {
	return tm.GetFilteredHistory(offset, limit, HistoryFilter{})
}

// GetFilteredHistory filters persisted task history before applying pagination.
func (tm *TaskManager) GetFilteredHistory(offset, limit int, filter HistoryFilter) ([]*Task, int) {
	if tm.historyErr != nil || tm.historyDB == nil {
		utils.ErrorWithFormat("查询下载历史失败，SQLite 不可用: %v", tm.historyErr)
		return []*Task{}, 0
	}
	items, total, err := loadTaskHistory(tm.historyDB, offset, limit, filter)
	if err != nil {
		utils.ErrorWithFormat("查询下载历史失败: %v", err)
		return []*Task{}, 0
	}
	return items, total
}

type DashboardMetrics struct {
	Pending      int `json:"pending"`
	Running      int `json:"running"`
	Completed24h int `json:"completed_24h"`
	Failed24h    int `json:"failed_24h"`
	TotalHistory int `json:"total_history"`
}

func (tm *TaskManager) DashboardMetrics(now time.Time) DashboardMetrics {
	metrics := DashboardMetrics{}
	tm.mu.Lock()
	for _, item := range tm.tasks {
		if item.Status == TaskStatusPending {
			metrics.Pending++
		} else if item.Status == TaskStatusRunning {
			metrics.Running++
		}
	}
	tm.mu.Unlock()

	if tm.historyErr != nil || tm.historyDB == nil {
		utils.ErrorWithFormat("读取下载历史指标失败，SQLite 不可用: %v", tm.historyErr)
		return metrics
	}
	historyMetrics, err := taskHistoryMetrics(tm.historyDB, now)
	if err != nil {
		utils.ErrorWithFormat("读取下载历史指标失败: %v", err)
		return metrics
	}
	metrics.Completed24h = historyMetrics.Completed24h
	metrics.Failed24h = historyMetrics.Failed24h
	metrics.TotalHistory = historyMetrics.TotalHistory
	return metrics
}

// CancelTask 取消任务
func (tm *TaskManager) CancelTask(id string) bool {
	tm.mu.Lock()
	t, ok := tm.tasks[id]
	if !ok {
		tm.mu.Unlock()
		return false
	}
	if t.Status == TaskStatusPending {
		t.Status = TaskStatusCancelled
		t.UpdatedAt = time.Now()
		snapshot := *t
		tm.mu.Unlock()
		tm.notifySubscribers(&snapshot)
		tm.moveToHistory(id)
		return true
	}
	tm.mu.Unlock()
	return false
}

// Subscribe 订阅任务事件
func (tm *TaskManager) Subscribe(taskID string) chan *Task {
	tm.subsMu.Lock()
	ch := make(chan *Task, 32)
	if _, ok := tm.subscribers[taskID]; !ok {
		tm.subscribers[taskID] = make(map[chan *Task]struct{})
	}
	tm.subscribers[taskID][ch] = struct{}{}
	tm.subsMu.Unlock()

	// Publish a current snapshot so subscribers cannot miss a task that finished
	// between the submit response and the EventSource connection.
	tm.mu.Lock()
	if current, ok := tm.tasks[taskID]; ok {
		snapshot := *current
		select {
		case ch <- &snapshot:
		default:
		}
		tm.mu.Unlock()
		return ch
	}
	tm.mu.Unlock()
	if tm.historyErr == nil && tm.historyDB != nil {
		if item, err := loadTaskHistoryByID(tm.historyDB, taskID); err == nil {
			select {
			case ch <- item:
			default:
			}
		}
	}
	return ch
}

// Unsubscribe 取消订阅
func (tm *TaskManager) Unsubscribe(taskID string, ch chan *Task) {
	tm.subsMu.Lock()
	defer tm.subsMu.Unlock()
	if subs, ok := tm.subscribers[taskID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(tm.subscribers, taskID)
		}
	}
}
