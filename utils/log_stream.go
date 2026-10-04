package utils

import (
	"strings"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"
)

const logHistoryCapacity = 5000

// LogEntry 一条进程内日志记录。ID 在单进程生命周期内递增，供日志查询游标使用。
type LogEntry struct {
	ID      uint64 `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Caller  string `json:"caller,omitempty"`
	Message string `json:"message"`
}

// LogPage 是基于游标的日志查询结果。
type LogPage struct {
	Items   []LogEntry `json:"items"`
	Cursor  uint64     `json:"cursor"`
	Dropped bool       `json:"dropped"`
}

// LogStreamManager 保存最近的日志。WebUI 使用短轮询读取快照，不持有长连接。
type LogStreamManager struct {
	mu      sync.RWMutex
	entries []LogEntry
	start   int
	count   int
	nextID  uint64
}

var logStreamMgr = newLogStreamManager(logHistoryCapacity)

func newLogStreamManager(capacity int) *LogStreamManager {
	if capacity < 1 {
		capacity = 1
	}
	return &LogStreamManager{entries: make([]LogEntry, capacity)}
}

func levelValue(level string) int {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	case "FATAL", "DPANIC", "PANIC":
		return 4
	default:
		return 1
	}
}

func (m *LogStreamManager) append(entry LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	entry.ID = m.nextID
	if m.count < len(m.entries) {
		index := (m.start + m.count) % len(m.entries)
		m.entries[index] = entry
		m.count++
		return
	}

	m.entries[m.start] = entry
	m.start = (m.start + 1) % len(m.entries)
}

func (m *LogStreamManager) read(after uint64, minLevel string, limit int) LogPage {
	if limit < 1 {
		limit = 300
	}
	if limit > 500 {
		limit = 500
	}
	minimum := levelValue(minLevel)

	m.mu.RLock()
	defer m.mu.RUnlock()

	page := LogPage{Items: make([]LogEntry, 0, limit), Cursor: m.nextID}
	if m.count == 0 {
		return page
	}

	oldest := m.entries[m.start].ID
	page.Dropped = after > 0 && after+1 < oldest

	if after == 0 {
		// 首次打开时只返回最近一页，避免一次性把整个缓存灌进浏览器。
		matches := make([]LogEntry, 0, m.count)
		for i := 0; i < m.count; i++ {
			entry := m.entries[(m.start+i)%len(m.entries)]
			if levelValue(entry.Level) >= minimum {
				matches = append(matches, entry)
			}
		}
		if len(matches) > limit {
			matches = matches[len(matches)-limit:]
		}
		page.Items = append(page.Items, matches...)
		return page
	}

	var hasMore bool
	for i := 0; i < m.count; i++ {
		entry := m.entries[(m.start+i)%len(m.entries)]
		if entry.ID <= after || levelValue(entry.Level) < minimum {
			continue
		}
		if len(page.Items) == limit {
			hasMore = true
			break
		}
		page.Items = append(page.Items, entry)
	}

	// 满页时保持在最后一条已发送记录，下次查询继续读取剩余数据。
	if hasMore && len(page.Items) > 0 {
		page.Cursor = page.Items[len(page.Items)-1].ID
	}
	return page
}

// ReadLogs 返回 after 之后、达到指定最低级别的日志。
func ReadLogs(after uint64, minLevel string, limit int) LogPage {
	return logStreamMgr.read(after, minLevel, limit)
}

// BroadcastLog 将 zap 日志写入有限容量的内存环形缓存。
func BroadcastLog(level string, caller string, msg string) {
	logStreamMgr.append(LogEntry{
		Time:    time.Now().Format(time.RFC3339),
		Level:   strings.ToUpper(level),
		Caller:  caller,
		Message: msg,
	})
}

// broadcastHook zap hook，把所有经过 logger 的日志写入 WebUI 可查询的缓存。
func broadcastHook(entry zapcore.Entry) error {
	BroadcastLog(entry.Level.CapitalString(), entry.Caller.TrimmedPath(), entry.Message)
	return nil
}
