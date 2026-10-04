<template>
  <section class="history-page">
    <PageHeading eyebrow="TASK JOURNAL / ARCHIVE" title="下载历史">
      <template #actions>
        <span class="total-count">{{ total }} 条记录</span>
      </template>
    </PageHeading>

    <n-card class="filter-card" size="small" :bordered="false">
      <n-space align="center" :size="10" wrap>
        <n-input v-model:value="draft.q" clearable placeholder="歌曲、歌手、链接或错误信息" style="width: 260px" @keyup.enter="applyFilters" />
        <n-select v-model:value="draft.platform" clearable placeholder="全部平台" :options="platformOptions" style="width: 145px" />
        <n-select v-model:value="draft.status" clearable placeholder="全部状态" :options="statusOptions" style="width: 145px" />
        <n-date-picker v-model:value="draft.range" type="daterange" clearable size="medium" start-placeholder="开始日期" end-placeholder="结束日期" />
        <n-button type="primary" :loading="loading" @click="applyFilters">查询</n-button>
        <n-button quaternary @click="clearFilters">重置</n-button>
      </n-space>
    </n-card>

    <n-data-table
      :columns="columns"
      :data="tasks"
      :loading="loading"
      :bordered="false"
      :row-key="(row: Task) => row.id"
      :max-height="620"
      :scroll-x="900"
    />
    <n-space justify="space-between" align="center" style="margin-top: 16px">
      <n-text depth="3" style="font-size: 11px">每 30 秒刷新一次，保留当前过滤条件</n-text>
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </n-space>
  </section>
</template>

<script setup lang="ts">
import { ref, onActivated, onDeactivated, onUnmounted, h } from 'vue'
import {
  NDataTable, NPagination, NSpace, NTag, NButton, NTooltip, NText,
  NInput, NSelect, NDatePicker, NCard, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { getTaskHistory } from '../api'
import PageHeading from '../components/PageHeading.vue'
import type { TaskHistoryFilters } from '../api'
import type { Task } from '../types'
import { watchTaskCompletion } from '../components/taskNotifications'
import { useTaskSubmission } from '../components/useTaskSubmission'

defineOptions({ name: 'History' })

const message = useMessage()
const { submitWithDuplicateConfirmation } = useTaskSubmission()
const loading = ref(true)
const tasks = ref<Task[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const draft = ref<{ q: string; platform: string | null; status: string | null; range: [number, number] | null }>({
  q: '', platform: null, status: null, range: null,
})
const filters = ref<TaskHistoryFilters>({})
let timer: ReturnType<typeof setInterval> | null = null

const platformOptions = [
  { label: '网易云', value: 'netease' }, { label: 'QQ 音乐', value: 'qq' },
  { label: 'YouTube', value: 'youtube' }, { label: 'B 站', value: 'bilibili' },
]
const statusOptions = [
  { label: '已完成', value: 'completed' }, { label: '失败', value: 'failed' }, { label: '已取消', value: 'cancelled' },
]

function tagType(status: string): 'default' | 'info' | 'success' | 'warning' | 'error' {
  switch (status) {
    case 'completed': return 'success'
    case 'failed': return 'error'
    case 'cancelled': return 'warning'
    default: return 'default'
  }
}
function platformLabel(value: string): string {
  return platformOptions.find((option) => option.value === value)?.label || value
}
function statusLabel(value: string): string {
  return statusOptions.find((option) => option.value === value)?.label || value
}
function formatTime(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString('zh-CN', { hour12: false })
}
function formatRangeBound(timestamp: number, endOfDay: boolean): string {
  const date = new Date(timestamp)
  if (endOfDay) date.setHours(23, 59, 59, 999)
  return date.toISOString()
}

async function handleRetry(row: Task) {
  try {
    const res = await submitWithDuplicateConfirmation(row.url)
    if (res.code === 200) {
      message.success('已重新提交')
      if (res.data?.id) watchTaskCompletion(res.data.id)
    } else if (res.code !== 499) message.error(res.message || '重试失败')
  } catch (error) {
    message.error(error instanceof Error ? `重试失败：${error.message}` : '重试失败')
  }
}
function applyFilters() {
  filters.value = {
    q: draft.value.q.trim() || undefined,
    platform: draft.value.platform || undefined,
    status: draft.value.status || undefined,
    from: draft.value.range ? formatRangeBound(draft.value.range[0], false) : undefined,
    to: draft.value.range ? formatRangeBound(draft.value.range[1], true) : undefined,
  }
  page.value = 1
  void load()
}
function clearFilters() {
  draft.value = { q: '', platform: null, status: null, range: null }
  filters.value = {}
  page.value = 1
  void load()
}

const columns: DataTableColumns<Task> = [
  {
    title: '平台', key: 'platform', width: 110,
    render: (row) => h(NTag, { size: 'small' }, { default: () => platformLabel(row.platform) }),
  },
  {
    title: '歌曲', key: 'song',
    render: (row) => row.song_info?.length
      ? h(NTooltip, { trigger: 'hover' }, {
          trigger: () => h('span', null, row.song_info!.map((song) => song.SongName).join(', ')),
          default: () => h('div', null, row.song_info!.map((song) => `${song.SongName} - ${song.SongArtists}`).join('\n')),
        })
      : h('span', { style: 'color:#777' }, row.url),
  },
  {
    title: '艺术家', key: 'artist', width: 140, ellipsis: { tooltip: true },
    render: (row) => row.song_info?.map((song) => song.SongArtists).join(', ') || '-',
  },
  {
    title: '状态', key: 'status', width: 90,
    render: (row) => h(NTag, { size: 'small', type: tagType(row.status) }, { default: () => statusLabel(row.status) }),
  },
  {
    title: '错误信息', key: 'error', width: 240,
    render: (row) => row.error
      ? h(NTooltip, { trigger: 'hover', placement: 'top' }, {
          trigger: () => h('span', { style: 'color:#e88080;font-size:12px' }, row.error!.slice(0, 50) + (row.error!.length > 50 ? '…' : '')),
          default: () => h('pre', { style: 'white-space:pre-wrap;font-size:11px;max-width:500px;margin:0' }, row.error),
        })
      : h('span', { style: 'color:#666' }, '-'),
  },
  { title: '时间', key: 'created_at', width: 170, render: (row) => formatTime(row.created_at) },
  {
    title: '操作', key: 'actions', width: 120,
    render: (row) => {
      const actions: any[] = []
      if (row.status === 'failed') actions.push(h(NButton, { size: 'tiny', type: 'info', quaternary: true, onClick: () => handleRetry(row) }, { default: () => '重试' }))
      return h(NSpace, { size: 4 }, { default: () => actions })
    },
  },
]

async function load() {
  loading.value = true
  try {
    const response = await getTaskHistory((page.value - 1) * pageSize, pageSize, filters.value)
    if (response.code === 200 && response.data) {
      tasks.value = response.data.items || []
      total.value = response.data.total || 0
    } else message.error(response.message || '加载下载历史失败')
  } catch (error) {
    message.error(error instanceof Error ? `加载下载历史失败：${error.message}` : '加载下载历史失败')
  } finally {
    loading.value = false
  }
}

function activateHistory() {
  if (timer) clearInterval(timer)
  void load()
  timer = setInterval(() => void load(), 30000)
}
function deactivateHistory() {
  if (timer) clearInterval(timer)
  timer = null
}

onActivated(activateHistory)
onDeactivated(deactivateHistory)
onUnmounted(deactivateHistory)
</script>

<style scoped>
.history-page { max-width: 1440px; margin: 0 auto; }
.total-count { color: #7b8882; font-size: 12px; }
.filter-card { margin-bottom: 14px; background: rgba(23,27,32,.75); }
</style>
