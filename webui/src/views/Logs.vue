<template>
  <div>
    <PageHeading eyebrow="SYSTEM / LIVE LOGS" title="日志" />
    <n-space vertical :size="12">
      <n-space align="center" justify="space-between" style="width: 100%">
        <n-space align="center" :size="8">
          <n-select v-model:value="level" :options="levelOptions" size="small" style="width: 120px" @update:value="handleLevelChange" />
          <n-input v-model:value="filterText" size="small" placeholder="过滤关键词…" style="width: 200px" clearable />
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="small" :aria-label="paused ? '继续滚动日志' : '暂停日志滚动'" @click="paused = !paused">
                <template #icon>
                  <n-icon><component :is="paused ? PlayOutline : PauseOutline" /></n-icon>
                </template>
              </n-button>
            </template>
            {{ paused ? '继续滚动' : '暂停滚动' }}
          </n-tooltip>
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="small" aria-label="清空当前日志" @click="clearLogs">
                <template #icon>
                  <n-icon><TrashOutline /></n-icon>
                </template>
              </n-button>
            </template>
            清空当前显示
          </n-tooltip>
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="small" aria-label="导出当前日志" @click="exportLogs" :disabled="logs.length === 0">
                <template #icon>
                  <n-icon><DownloadOutline /></n-icon>
                </template>
              </n-button>
            </template>
            {{ logs.length === 0 ? '暂无日志可导出' : '导出当前日志为 .log 文件' }}
          </n-tooltip>
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="small" aria-label="立即同步日志" @click="refreshLogs" :loading="connecting">
                <template #icon>
                  <n-icon><RefreshOutline /></n-icon>
                </template>
              </n-button>
            </template>
            立即同步日志
          </n-tooltip>
        </n-space>
        <n-space align="center" :size="8">
          <n-text depth="3" style="font-size: 12px">
            {{ filteredLogs.length }}{{ filterText ? '/' + logs.length : '' }} 条
          </n-text>
          <n-tag :type="connected ? 'success' : (connecting ? 'warning' : 'error')" size="small">
            {{ connected ? '实时同步' : (connecting ? '正在同步...' : '同步失败，稍后重试') }}
          </n-tag>
        </n-space>
      </n-space>
      <n-alert v-if="droppedLogs" type="warning" :show-icon="false">
        日志缓存已滚动覆盖部分未读取记录，当前已从最新可用位置继续同步。
      </n-alert>
      <n-card size="small" content-style="height: 100%; overflow: auto; padding: 8px 12px" :style="{ height: 'calc(100vh - 240px)' }">
        <div ref="logContainer" style="font-family: 'SF Mono', Monaco, monospace; font-size: 12px; line-height: 1.7">
          <div v-for="log in displayLogs" :key="logKey(log)" :style="{ color: levelColor(log.level) }">
            <span style="opacity: 0.6">{{ formatTime(log.time) }}</span>
            <n-tag :type="levelTag(log.level)" size="tiny" style="margin: 0 6px; font-size: 10px">{{ log.level }}</n-tag>
            <span style="opacity: 0.6">{{ log.caller }}</span>
            <span style="margin-left: 8px; user-select: text">{{ log.message }}</span>
          </div>
          <div v-if="displayLogs.length === 0" style="opacity: 0.5; text-align: center; padding: 40px">
            {{ connected ? (filterText ? '无匹配日志' : '暂无新日志') : (connecting ? '正在读取日志...' : '日志暂时不可用，稍后自动重试') }}
          </div>
        </div>
      </n-card>
    </n-space>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { NCard, NSpace, NSelect, NButton, NTag, NText, NInput, NTooltip, NIcon, NAlert } from 'naive-ui'
import { PlayOutline, PauseOutline, TrashOutline, DownloadOutline, RefreshOutline } from '@vicons/ionicons5'
import PageHeading from '../components/PageHeading.vue'

interface LogEntry {
  id: number
  time: string
  level: string
  caller?: string
  message: string
}

interface LogPage {
  items: LogEntry[]
  cursor: number
  dropped: boolean
}

interface ApiResponse<T> {
  code: number
  message: string
  data?: T
}

const MAX_LOGS = 1000
const POLL_INTERVAL_MS = 1000
const MAX_RETRY_MS = 15000
const level = ref('info')
const paused = ref(false)
const logs = ref<LogEntry[]>([])
const filterText = ref('')
const connected = ref(false)
const connecting = ref(true)
const droppedLogs = ref(false)
const logContainer = ref<HTMLElement | null>(null)
const cursor = ref(0)
let pollTimer: ReturnType<typeof setTimeout> | null = null
let controller: AbortController | null = null
let generation = 0
let retryAttempts = 0
let disposed = false

const levelOptions = [
  { label: 'DEBUG', value: 'debug' },
  { label: 'INFO', value: 'info' },
  { label: 'WARN', value: 'warn' },
  { label: 'ERROR', value: 'error' },
]

const filteredLogs = computed(() => {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return logs.value
  return logs.value.filter(
    (l) =>
      l.message.toLowerCase().includes(q) ||
      l.level.toLowerCase().includes(q) ||
      (l.caller || '').toLowerCase().includes(q),
  )
})

const displayLogs = computed(() => {
  const list = paused.value ? filteredLogs.value.slice(-100) : filteredLogs.value.slice(-300)
  return list
})

function logKey(log: LogEntry): number {
  return log.id
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso.slice(11, 19) || iso
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}

function levelTag(level: string): 'default' | 'info' | 'warning' | 'error' {
  switch (level) {
    case 'ERROR': case 'FATAL': return 'error'
    case 'WARN': return 'warning'
    case 'DEBUG': return 'default'
    default: return 'info'
  }
}

function levelColor(level: string): string {
  switch (level) {
    case 'ERROR': case 'FATAL': return '#e88080'
    case 'WARN': return '#f2c97d'
    case 'DEBUG': return '#7f8c8d'
    default: return '#c2c8ce'
  }
}

function scrollToBottom() {
  if (paused.value) return
  nextTick(() => {
    const el = logContainer.value?.parentElement
    if (el) el.scrollTop = el.scrollHeight
  })
}

function appendLogs(items: LogEntry[]) {
  if (items.length === 0) return
  logs.value.push(...items)
  if (logs.value.length > MAX_LOGS) {
    logs.value = logs.value.slice(-MAX_LOGS)
  }
  scrollToBottom()
}

function exportLogs() {
  const text = logs.value
    .map((l) => `${l.time}\t${l.level}\t${l.caller || ''}\t${l.message}`)
    .join('\n')
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `gymdl-logs-${Date.now()}.log`
  a.click()
  URL.revokeObjectURL(url)
}

function scheduleNext(delay: number, currentGeneration: number) {
  if (disposed || currentGeneration !== generation) return
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => {
    pollTimer = null
    void pollLogs(currentGeneration)
  }, delay)
}

async function pollLogs(currentGeneration: number) {
  if (disposed || currentGeneration !== generation) return
  const requestController = new AbortController()
  controller = requestController
  let timedOut = false
  const requestTimeout = setTimeout(() => {
    timedOut = true
    requestController.abort()
  }, 10000)

  try {
    const params = new URLSearchParams({
      after: String(cursor.value),
      level: level.value,
      limit: '500',
    })
    const response = await fetch(`/api/web/logs?${params}`, {
      headers: { Accept: 'application/json' },
      signal: requestController.signal,
      cache: 'no-store',
    })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)

    const result = await response.json() as ApiResponse<LogPage>
    if (result.code !== 200 || !result.data) {
      throw new Error(result.message || '日志接口返回异常')
    }
    if (disposed || currentGeneration !== generation) return

    const page = result.data
    if (!Array.isArray(page.items) || !Number.isFinite(page.cursor)) {
      throw new Error('日志数据格式异常')
    }
    appendLogs(page.items)
    cursor.value = page.cursor
    droppedLogs.value ||= page.dropped
    connected.value = true
    connecting.value = false
    retryAttempts = 0
    scheduleNext(POLL_INTERVAL_MS, currentGeneration)
  } catch {
    if ((requestController.signal.aborted && !timedOut) || disposed || currentGeneration !== generation) return
    connected.value = false
    connecting.value = false
    retryAttempts++
    const delay = Math.min(1000 * (2 ** Math.min(retryAttempts - 1, 4)), MAX_RETRY_MS)
    scheduleNext(delay, currentGeneration)
  } finally {
    clearTimeout(requestTimeout)
    if (controller === requestController) controller = null
  }
}

function startPolling(resetCursor: boolean) {
  generation++
  const currentGeneration = generation
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
  controller?.abort()
  controller = null
  if (resetCursor) {
    cursor.value = 0
    droppedLogs.value = false
  }
  retryAttempts = 0
  connected.value = false
  connecting.value = true
  void pollLogs(currentGeneration)
}

function refreshLogs() {
  startPolling(false)
}

function handleLevelChange() {
  logs.value = []
  filterText.value = ''
  startPolling(true)
}

function clearLogs() {
  logs.value = []
}

onMounted(() => startPolling(false))
onUnmounted(() => {
  disposed = true
  generation++
  if (pollTimer) clearTimeout(pollTimer)
  controller?.abort()
})
</script>
