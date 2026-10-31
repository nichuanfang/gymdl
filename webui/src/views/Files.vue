<template>
  <section class="files-page">
    <PageHeading eyebrow="LIBRARY / TIDY DESTINATION" title="文件库">
      <template #actions>
        <n-tag v-if="target" :bordered="false" type="info">{{ target === 'webdav' ? 'WebDAV 整理目录' : '本地整理目录' }}</n-tag>
      </template>
    </PageHeading>

    <n-space vertical :size="14">
      <div class="path-toolbar">
        <n-button quaternary size="small" @click="goUp" :disabled="!currentPath || !!appliedQuery">← 上级</n-button>
        <span class="path-chip">/{{ currentPath }}</span>
      </div>

      <n-card size="small" :bordered="false" class="search-card">
        <n-space align="center" :size="9" wrap>
          <n-input-group class="library-search">
            <n-input v-model:value="queryDraft" clearable placeholder="搜索歌曲名、歌手或专辑" @keyup.enter="searchLibrary" />
            <n-button type="primary" :loading="searching" :disabled="loading" @click="searchLibrary">搜索文件库</n-button>
          </n-input-group>
          <n-select v-model:value="fieldDraft" :options="fieldOptions" class="field-select" />
          <n-button quaternary :disabled="!appliedQuery && !queryDraft" @click="clearSearch">清除搜索</n-button>
        </n-space>
        <div v-if="appliedQuery" class="search-hint">
          在 <strong>{{ currentPath ? `/${currentPath}` : '整理目录根目录' }}</strong> 及其子目录中搜索“{{ appliedQuery }}”，按更新时间从新到旧排列。
        </div>
      </n-card>

      <div class="quick-filters" role="toolbar" aria-label="文件库快速筛选">
        <button v-for="item in quickFilters" :key="item.value" type="button" class="quick-filter"
                :class="{ active: quickFilter === item.value }" @click="setQuickFilter(item.value)">
          {{ item.label }}<span v-if="item.count !== undefined" class="quick-count">{{ item.count }}</span>
        </button>
        <span class="quick-summary">{{ summaryText }}</span>
      </div>

      <n-alert v-if="truncated" type="warning" :show-icon="false">
        当前搜索结果过多，仅展示更新时间较新的前 {{ maxResults }} 个文件；可进入更具体的目录缩小范围。
      </n-alert>
      <n-alert v-if="pendingUploads.length > 0" type="warning" :show-icon="false">
        <div class="pending-heading">
          <span>NAS 暂不可用，{{ pendingUploads.length }} 个文件已安全保存在本地，网络恢复后会自动补传。</span>
          <n-button size="tiny" @click="refreshPending">刷新</n-button>
        </div>
        <ul class="pending-list">
          <li v-for="item in pendingUploads.slice(0, 5)" :key="item.id">
            {{ item.file_name }} · {{ item.attempts }} 次尝试
            <span v-if="item.last_error" class="pending-error">{{ item.last_error }}</span>
          </li>
        </ul>
      </n-alert>
      <n-alert v-if="loadError" type="error" :show-icon="false">
        <n-space justify="space-between" align="center">
          <span>{{ loadError }}</span>
          <n-button size="tiny" @click="load(currentPath || '/')">重试</n-button>
        </n-space>
      </n-alert>
      <n-data-table
        v-if="entries.length > 0 || (!loadError && loading)"
        :columns="columns"
        :data="visibleEntries"
        :bordered="false"
        :row-key="(row: FileEntry) => row.path"
        :scroll-x="1120"
      />
      <div v-if="filteredEntries.length > filePageSize" class="file-pagination">
        <n-pagination v-model:page="filePage" :page-size="filePageSize" :item-count="filteredEntries.length" />
      </div>
      <n-empty
        v-else-if="!loadError"
        style="margin-top: 44px"
        :description="appliedQuery ? '当前目录及其子目录没有匹配的音频文件' : (currentPath ? '此目录为空' : '整理目录为空')"
      >
        <template #extra>
          <n-button v-if="appliedQuery" size="small" @click="clearSearch">清除搜索条件</n-button>
          <n-button v-else-if="!currentPath" size="small" type="primary" @click="$router.push('/search')">去搜索下载</n-button>
        </template>
      </n-empty>
    </n-space>

    <n-modal v-model:show="previewOpen" preset="card" :title="selectedFile?.name || '音频预览'" class="preview-modal" :bordered="false">
      <div class="preview-body">
        <div class="preview-art">♫</div>
        <p class="preview-name">{{ selectedFile?.name }}</p>
        <p class="preview-subtitle">{{ selectedFile?.ext?.replace('.', '').toUpperCase() }} · {{ selectedFile ? formatSize(selectedFile.size) : '' }}</p>
        <audio
          v-if="selectedFile"
          :key="previewUrl"
          ref="audioElement"
          class="audio-player"
          :src="previewUrl"
          controls
          preload="metadata"
          @canplay="playbackError = false"
          @error="playbackError = true"
        />
        <n-alert v-if="playbackError" type="warning" :show-icon="false" style="margin-top: 14px">
          浏览器无法预览此格式或音频服务暂不可用，可下载原文件后使用本地播放器打开。
        </n-alert>
        <n-space justify="center" style="margin-top: 16px">
          <n-button tag="a" :href="previewUrl" :download="selectedFile?.name" secondary>下载原文件</n-button>
        </n-space>
      </div>
    </n-modal>
  </section>
</template>

<script setup lang="ts">
import { ref, computed, h, nextTick, watch, onActivated, onDeactivated } from 'vue'
import {
  NDataTable, NPagination, NButton, NSpace, NInput, NInputGroup, NSelect, NEmpty, NTag, NModal, NAlert, NPopconfirm, NCard, NTooltip, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { listFiles, getFileStreamUrl, deleteFile, getPendingWebDAVUploads } from '../api'
import PageHeading from '../components/PageHeading.vue'
import type { FileEntry, PendingWebDAVUpload } from '../types'

const message = useMessage()
const loading = ref(true)
const searching = ref(false)
const loadError = ref('')
const entries = ref<FileEntry[]>([])
const filePage = ref(1)
const filePageSize = 50
const visibleEntries = computed(() => {
  const start = (filePage.value - 1) * filePageSize
  return filteredEntries.value.slice(start, start + filePageSize)
})
const currentPath = ref('')
const queryDraft = ref('')
const fieldDraft = ref('all')
const appliedQuery = ref('')
const appliedField = ref('all')
const target = ref<'local' | 'webdav' | ''>('')
const truncated = ref(false)
const pendingUploads = ref<PendingWebDAVUpload[]>([])
const maxResults = 5000
const quickFilter = ref<'all' | 'recent' | 'lossless' | 'large'>('all')
const lastPathKey = 'gymdl_files_path'
const previewOpen = ref(false)
const audioElement = ref<HTMLAudioElement | null>(null)
const selectedFile = ref<FileEntry | null>(null)
const playbackError = ref(false)
const previewUrl = computed(() => selectedFile.value ? getFileStreamUrl(selectedFile.value.path) : '')
const fieldOptions = [
  { label: '全部信息', value: 'all' },
  { label: '歌曲名称', value: 'song' },
  { label: '歌手', value: 'artist' },
  { label: '专辑', value: 'album' },
]
const audioEntries = computed(() => entries.value.filter((entry) => !entry.is_dir))
const quickFilters = computed(() => [
  { label: '全部', value: 'all' as const, count: audioEntries.value.length },
  { label: '最近 7 天', value: 'recent' as const, count: audioEntries.value.filter(isRecent).length },
  { label: '无损', value: 'lossless' as const, count: audioEntries.value.filter(isLossless).length },
  { label: '大于 30 MB', value: 'large' as const, count: audioEntries.value.filter((entry) => entry.size > 30 * 1024 * 1024).length },
])
const filteredEntries = computed(() => {
  if (quickFilter.value === 'all') return entries.value
  return entries.value.filter((entry) => {
    if (entry.is_dir) return quickFilter.value === 'all'
    if (quickFilter.value === 'recent') return isRecent(entry)
    if (quickFilter.value === 'lossless') return isLossless(entry)
    return entry.size > 30 * 1024 * 1024
  })
})
const summaryText = computed(() => {
  const total = audioEntries.value.length
  if (!total) return '当前目录暂无音频'
  const size = audioEntries.value.reduce((sum, entry) => sum + entry.size, 0)
  return `${total} 首 · ${formatSize(size)}`
})

const parentPath = computed(() => {
  const parts = currentPath.value.split('/').filter(Boolean)
  parts.pop()
  return parts.join('/')
})

function formatSize(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`
  return `${(value / 1024 ** 3).toFixed(2)} GB`
}
function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { hour12: false })
}
function isRecent(entry: FileEntry) {
  const time = new Date(entry.mod_time).getTime()
  return Number.isFinite(time) && Date.now() - time <= 7 * 24 * 60 * 60 * 1000
}
function isLossless(entry: FileEntry) {
  return ['.flac', '.wav', '.alac', '.ape'].includes(entry.ext.toLowerCase())
}
function setQuickFilter(value: 'all' | 'recent' | 'lossless' | 'large') {
  quickFilter.value = value
  filePage.value = 1
}
function preview(entry: FileEntry) {
  selectedFile.value = entry
  playbackError.value = false
  previewOpen.value = true
  void nextTick(() => audioElement.value?.play().catch((error: DOMException) => {
    if (error.name !== 'NotAllowedError') playbackError.value = true
  }))
}
function enterDirectory(path: string) {
  queryDraft.value = ''
  appliedQuery.value = ''
  fieldDraft.value = 'all'
  appliedField.value = 'all'
  void load(path)
}

const columns: DataTableColumns<FileEntry> = [
  {
    title: '名称', key: 'name', minWidth: 220,
    render: (row) => row.is_dir
      ? h('button', { class: 'directory-link', type: 'button', onClick: () => enterDirectory(row.path) }, `▸  ${row.name}`)
      : h(NTooltip, { trigger: 'hover' }, {
          trigger: () => h('span', { class: 'file-name' }, `♫  ${row.name}`),
          default: () => [row.artist, row.album].filter(Boolean).join(' · ') || row.path,
        }),
  },
  {
    title: '歌手', key: 'artist', width: 145, ellipsis: { tooltip: true },
    render: (row) => row.is_dir ? '—' : (row.artist || '—'),
  },
  {
    title: '专辑', key: 'album', width: 145, ellipsis: { tooltip: true },
    render: (row) => row.is_dir ? '—' : (row.album || '—'),
  },
  { title: '整理路径', key: 'path', width: 250, ellipsis: { tooltip: true }, render: (row) => row.is_dir ? '—' : row.path },
  { title: '大小', key: 'size', width: 100, render: (row) => row.is_dir ? '—' : formatSize(row.size) },
  { title: '格式', key: 'ext', width: 75, render: (row) => row.is_dir ? '' : row.ext.replace('.', '').toUpperCase() },
  { title: '更新时间', key: 'mod_time', width: 160, sorter: (a, b) => new Date(a.mod_time).getTime() - new Date(b.mod_time).getTime(), render: (row) => formatTime(row.mod_time) },
  {
    title: '操作', key: 'actions', width: 145, fixed: 'right',
    render: (row) => row.is_dir ? null : h(NSpace, { size: 4, wrap: false }, {
      default: () => [
        h(NButton, { size: 'small', quaternary: true, type: 'primary', onClick: () => preview(row) }, { default: () => '播放' }),
        h(NPopconfirm, {
          positiveText: '删除', negativeText: '取消', onPositiveClick: () => void handleDelete(row),
        }, {
          trigger: () => h(NButton, { size: 'small', quaternary: true, type: 'error' }, { default: () => '删除' }),
          default: () => `确定从${target.value === 'webdav' ? ' WebDAV ' : '本地 '}整理目录删除“${row.name}”吗？`,
        }),
      ],
    }),
  },
]

async function load(path = '/', options: { showPageLoading?: boolean; preserveEntriesOnError?: boolean } = {}): Promise<boolean> {
  const showPageLoading = options.showPageLoading ?? true
  if (showPageLoading) loading.value = true
  loadError.value = ''
  try {
    const response = await listFiles(path, appliedQuery.value, appliedField.value)
    if (response.code === 200 && response.data) {
      entries.value = response.data.entries || []
      filePage.value = 1
      currentPath.value = (response.data.path || '').replace(/^\/+|\/+$/g, '')
      localStorage.setItem(lastPathKey, currentPath.value || '/')
      target.value = response.data.target || 'local'
      truncated.value = !!response.data.truncated
      return true
    } else {
      if (!options.preserveEntriesOnError) entries.value = []
      loadError.value = response.message || '读取文件库失败'
      return false
    }
  } catch (error) {
    if (!options.preserveEntriesOnError) entries.value = []
    loadError.value = error instanceof Error ? `读取文件库失败：${error.message}` : '读取文件库失败'
    return false
  } finally {
    if (showPageLoading) loading.value = false
  }
}
async function searchLibrary() {
  if (loading.value || searching.value) return
  const previousQuery = appliedQuery.value
  const previousField = appliedField.value
  appliedQuery.value = queryDraft.value.trim()
  appliedField.value = fieldDraft.value
  searching.value = true
  try {
    const loaded = await load(currentPath.value || '/', { showPageLoading: false, preserveEntriesOnError: true })
    if (!loaded) {
      appliedQuery.value = previousQuery
      appliedField.value = previousField
    }
  } finally {
    searching.value = false
  }
}
async function clearSearch() {
  queryDraft.value = ''
  appliedQuery.value = ''
  fieldDraft.value = 'all'
  appliedField.value = 'all'
  await load(currentPath.value || '/')
}
watch(previewOpen, (open) => {
  if (!open && audioElement.value) {
    audioElement.value.pause()
    audioElement.value.currentTime = 0
  }
})
function deactivateFiles() {
  previewOpen.value = false
  audioElement.value?.pause()
}
function goUp() { enterDirectory(parentPath.value || '/') }
async function handleDelete(row: FileEntry) {
  try {
    const response = await deleteFile(row.path)
    if (response.code === 200) {
      message.success('文件已删除')
      if (selectedFile.value?.path === row.path) previewOpen.value = false
      await load(currentPath.value || '/')
    } else message.error(response.message || '删除失败')
  } catch (error) {
    message.error(error instanceof Error ? `删除失败：${error.message}` : '删除失败')
  }
}

async function refreshPending() {
  try {
    const response = await getPendingWebDAVUploads()
    if (response.code === 200 && response.data) pendingUploads.value = response.data.items || []
  } catch {
    // 文件库本身仍可正常浏览，队列状态失败不覆盖当前错误提示。
  }
}

onActivated(() => {
  const remembered = currentPath.value || localStorage.getItem(lastPathKey) || '/'
  void load(remembered)
  void refreshPending()
})
onDeactivated(deactivateFiles)
</script>

<style scoped>
.files-page { max-width: 1480px; margin: 0 auto; }
.file-pagination { display: flex; justify-content: flex-end; margin-top: 14px; }
.path-toolbar { display: flex; align-items: center; gap: 10px; min-height: 32px; }
.path-chip { color: #7e8985; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; overflow-wrap: anywhere; }
.search-card { background: rgba(23,27,32,.75); }
.library-search { width: min(100%, 520px); }
.field-select { width: 130px; }
.search-hint { margin-top: 10px; color: #788580; font-size: 11px; }
.search-hint strong { color: #b8c8c1; font-weight: 500; }
.quick-filters { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; padding: 2px 0; }
.quick-filter { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 4px 11px; border: 1px solid rgba(171,190,184,.14); border-radius: 999px; color: #8c9a94; background: rgba(20,24,29,.7); font-size: 11px; cursor: pointer; transition: color .18s, border-color .18s, background .18s, transform .18s; }
.quick-filter:hover { color: #d5e5df; border-color: rgba(99,226,183,.35); transform: translateY(-1px); }
.quick-filter.active { color: #06140f; border-color: #63e2b7; background: #63e2b7; }
.quick-count { min-width: 16px; padding: 0 5px; border-radius: 999px; background: rgba(255,255,255,.1); font-size: 9px; line-height: 16px; text-align: center; }
.quick-filter.active .quick-count { background: rgba(6,20,15,.18); }
.quick-summary { margin-left: auto; color: #65736d; font-size: 10px; }
.pending-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.pending-list { margin: 8px 0 0; padding-left: 18px; color: #a7b2ad; font-size: 11px; }
.pending-list li { margin-top: 4px; overflow-wrap: anywhere; }
.pending-error { color: #c98f76; }
:global(.files-page .directory-link) { appearance: none; padding: 0; border: 0; background: transparent; color: #75d8b4; cursor: pointer; font: inherit; text-align: left; }
:global(.files-page .directory-link:hover) { color: #a4f5d6; }
:global(.files-page .directory-link:focus-visible) { border-radius: 2px; outline: 2px solid #63e2b7; outline-offset: 3px; }
.file-name { color: #d9e2df; }
:global(.n-card.preview-modal) { width: min(520px, calc(100vw - 32px)); }
.preview-body { padding: 8px 4px 4px; text-align: center; }
.preview-art { display: grid; place-items: center; width: 112px; height: 112px; margin: 0 auto 18px; border: 1px solid rgba(99,226,183,.26); border-radius: 26px; color: #63e2b7; background: radial-gradient(circle at 30% 25%, rgba(99,226,183,.22), rgba(30,40,39,.4) 65%); font-size: 52px; }
.preview-name { margin: 0; color: #edf4f1; font-size: 15px; font-weight: 600; overflow-wrap: anywhere; }
.preview-subtitle { margin: 7px 0 20px; color: #7d8984; font-size: 10px; letter-spacing: .12em; }
.audio-player { width: 100%; height: 42px; }
@media (max-width: 700px) {
  .library-search { width: 100%; }
  .field-select { width: 100%; }
}
</style>
