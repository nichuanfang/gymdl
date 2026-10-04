<template>
  <section class="search-page">
    <header class="page-heading">
      <n-h2>搜索</n-h2>
    </header>

    <n-space vertical :size="8" :wrap-item="false">
      <div class="search-toolbar">
        <n-input-group class="keyword-group">
          <n-input
            v-model:value="keyword"
            placeholder="输入歌曲 / 歌手 / 视频关键词"
            size="large"
            @focus="selectKeyword"
            @keyup.enter="startSearch"
          />
          <n-button type="primary" size="large" :loading="loadingAction === 'search'" :disabled="searching" @click="startSearch">搜索</n-button>
        </n-input-group>
        <n-checkbox-group v-model:value="selectedPlatforms" class="platform-selectors">
          <n-checkbox value="netease" label="网易云" />
          <n-checkbox value="qq" label="QQ 音乐" :disabled="!qqEnabled" />
          <n-checkbox value="youtube" label="YouTube" />
          <n-checkbox value="bilibili" label="B 站" />
        </n-checkbox-group>
        <div class="platform-actions">
          <n-button quaternary size="small" :disabled="searching" @click="togglePlatforms">{{ allPlatformsSelected ? '反选' : '全选' }}</n-button>
        </div>
      </div>

      <n-alert v-if="activeErrors.length" type="warning" :show-icon="false">
        部分平台搜索失败：{{ activeErrors.join('；') }}。已返回其他平台结果。
      </n-alert>

      <div v-if="searched && results.length" class="result-toolbar">
        <div class="filter-controls">
          <n-input v-model:value="resultQuery" clearable size="small" placeholder="筛选歌曲 / 歌手 / 专辑" class="filter-query" />
          <n-select v-model:value="resultPlatform" size="small" :options="resultPlatformOptions" class="filter-platform" />
          <n-select v-model:value="vipFilter" size="small" :options="vipOptions" class="filter-vip" />
        </div>
      </div>
      <n-data-table
        :columns="columns"
        :data="filteredResults"
        size="small"
        :bordered="false"
        :row-key="rowKey"
        :scroll-x="1164"
      />

      <div v-if="searched" class="pagination-bar">
        <span class="pagination-summary">本页 {{ filteredResults.length }} 条 <span class="summary-muted">/ {{ results.length }} 条</span></span>
        <div class="pagination-controls">
          <n-select
            v-model:value="batchSize"
            size="small"
            :options="batchOptions"
            class="batch-select"
            :disabled="searching"
            :loading="loadingAction === 'size'"
            @update:value="changePageSize"
          />
          <n-button
            secondary
            :disabled="currentPage <= 1 || searching || searchRequestDirty"
            :loading="loadingAction === 'previous'"
            @click="goToPage(currentPage - 1)"
          >上一页</n-button>
          <n-tag :bordered="false" type="info">{{ currentPage }}</n-tag>
          <n-button
            secondary
            :disabled="!hasMore || searching || searchRequestDirty || currentPage * batchSize > 2000"
            :loading="loadingAction === 'next'"
            @click="goToPage(currentPage + 1)"
          >下一页</n-button>
        </div>
      </div>

      <n-empty v-if="!searching && searched && results.length === 0" description="没有找到相关结果" style="margin-top: 32px" />
      <n-empty v-else-if="!searching && searched && results.length > 0 && filteredResults.length === 0" description="当前筛选条件下没有匹配结果" style="margin-top: 20px" />

      <div v-if="searchHistory.length" class="history-strip">
        <span class="history-label">最近搜索</span>
        <div class="history-items">
          <n-tag v-for="item in searchHistory" :key="item" size="small" class="history-tag" @click="keyword = item; startSearch()">{{ item }}</n-tag>
        </div>
        <n-button quaternary size="tiny" class="history-clear" @click="clearHistory">清空</n-button>
      </div>
    </n-space>
  </section>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import {
  NH2, NInputGroup, NInput, NButton, NSpace, NTag, NDataTable, NAlert,
  NEmpty, NCheckbox, NCheckboxGroup, NTooltip, NSelect, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { get } from '../api/http'
import { watchTaskCompletion } from '../components/taskNotifications'
import { useTaskSubmission } from '../components/useTaskSubmission'
import type { SearchResultItem } from '../types/search'

type SearchRow = SearchResultItem

const HISTORY_KEY = 'gymdl_search_history'
const PLATFORM_ORDER = ['netease', 'qq', 'youtube', 'bilibili']
const message = useMessage()
const { submitWithDuplicateConfirmation } = useTaskSubmission()
const keyword = ref('')
const lastSearchedKeyword = ref('')
const searching = ref(false)
const loadingAction = ref<'search' | 'previous' | 'next' | 'size' | ''>('')
const searched = ref(false)
const results = ref<SearchRow[]>([])
const errors = ref<Record<string, string>>({})
const hasMore = ref(false)
const currentPage = ref(1)
const lastSearchedPlatforms = ref<string[]>([])
const qqEnabled = ref(true)
const platformConfigReady = ref(false)
const searchHistory = ref<string[]>([])
const resultQuery = ref('')
const resultPlatform = ref('all')
const vipFilter = ref('all')
const batchSize = ref(10)
const batchOptions = [10, 20, 50, 100].map((value) => ({ label: `每页 ${value} 首`, value }))
const resultPlatformOptions = computed(() => [
  { label: '全部平台', value: 'all' },
  { label: '网易云', value: 'netease' },
  { label: 'QQ 音乐', value: 'qq' },
  { label: 'YouTube', value: 'youtube' },
  { label: 'B 站', value: 'bilibili' },
])
const vipOptions = [
  { label: '全部结果', value: 'all' },
  { label: '仅免费', value: 'free' },
  { label: '仅 VIP', value: 'vip' },
]
const selectedPlatforms = ref<string[]>(['netease', 'qq', 'youtube', 'bilibili'])
const chosenPlatforms = computed(() => PLATFORM_ORDER.filter((platform) => selectedPlatforms.value.includes(platform) && (platform !== 'qq' || qqEnabled.value)))
const searchRequestDirty = computed(() =>
  keyword.value.trim() !== lastSearchedKeyword.value || chosenPlatforms.value.join(',') !== lastSearchedPlatforms.value.join(','),
)
const activeErrors = computed(() => Object.entries(errors.value).map(([platform, error]) => `${platformLabel(platform)}：${error}`))
const filteredResults = computed(() => {
  const query = resultQuery.value.trim().toLocaleLowerCase()
  return results.value.filter((item) => {
    const textMatches = !query || [item.name, item.artists, item.album, item.url].some((field) => field?.toLocaleLowerCase().includes(query))
    const platformMatches = resultPlatform.value === 'all' || item.platform === resultPlatform.value
    const vipMatches = vipFilter.value === 'all' || (vipFilter.value === 'vip' ? item.is_vip : !item.is_vip)
    return textMatches && platformMatches && vipMatches
  })
})
const allPlatformsSelected = computed(() => {
  const enabled = PLATFORM_ORDER.filter((platform) => platform !== 'qq' || qqEnabled.value)
  return enabled.length > 0 && enabled.every((platform) => selectedPlatforms.value.includes(platform))
})

function rowKey(row: SearchResultItem) { return `${row.platform}:${row.song_id}` }
function platformLabel(platform: string): string {
  return ({ netease: '网易云', qq: 'QQ 音乐', youtube: 'YouTube', bilibili: 'B 站' } as Record<string, string>)[platform] || platform
}
function formatDuration(sec: number): string {
  if (!sec) return '-'
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
}
function togglePlatforms() {
  selectedPlatforms.value = allPlatformsSelected.value ? [] : PLATFORM_ORDER.filter((platform) => platform !== 'qq' || qqEnabled.value)
}
function selectKeyword(event: FocusEvent) {
  const input = event.target
  if (input instanceof HTMLInputElement) input.select()
}
function loadHistory() {
  try { searchHistory.value = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]') } catch { /* ignore */ }
}
function saveHistory(query: string) {
  const next = [query, ...searchHistory.value.filter((item) => item !== query)].slice(0, 10)
  searchHistory.value = next
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(next)) } catch { /* ignore */ }
}
function clearHistory() {
  searchHistory.value = []
  try { localStorage.removeItem(HISTORY_KEY) } catch { /* ignore */ }
}
async function startSearch() {
  if (searching.value) return
  const query = keyword.value.trim()
  if (!query) {
    message.warning('请输入关键词')
    return
  }
  if (!platformConfigReady.value) await platformConfigPromise
  const platforms = chosenPlatforms.value
  if (!platforms.length) {
    message.warning('请至少选择一个平台')
    return
  }

  if (query !== lastSearchedKeyword.value) {
    resultQuery.value = ''
    resultPlatform.value = 'all'
    vipFilter.value = 'all'
  }
  searched.value = true
  await loadPage(1, query, platforms, 'search')
}

async function goToPage(page: number) {
  if (searching.value || searchRequestDirty.value || page < 1) return
  if (page > currentPage.value && !hasMore.value) return
  await loadPage(page, lastSearchedKeyword.value, lastSearchedPlatforms.value, page < currentPage.value ? 'previous' : 'next')
}

async function changePageSize(size: number) {
  batchSize.value = size
  if (searched.value && lastSearchedKeyword.value && lastSearchedPlatforms.value.length && !searchRequestDirty.value) {
    await loadPage(1, lastSearchedKeyword.value, lastSearchedPlatforms.value, 'size')
  }
}

async function loadPage(page: number, query: string, platforms: string[], action: 'search' | 'previous' | 'next' | 'size') {
  searching.value = true
  loadingAction.value = action
  try {
    const offset = (page - 1) * batchSize.value
    const res = await get<{ items: SearchResultItem[]; total: number; has_more: boolean; errors: Record<string, string>; keyword: string }>(
      '/search', { keyword: query, platform: platforms.join(','), offset, limit: batchSize.value },
    )
    if (res.code !== 200 || !res.data) {
      message.error(res.message || '搜索失败')
      return
    }
    results.value = res.data.items || []
    lastSearchedKeyword.value = query
    lastSearchedPlatforms.value = [...platforms]
    hasMore.value = !!res.data.has_more
    errors.value = res.data.errors || {}
    currentPage.value = page
    searched.value = true
    if (page === 1) saveHistory(query)
  } catch (error) {
    message.error(error instanceof Error ? `搜索失败：${error.message}` : '搜索失败')
  } finally {
    searching.value = false
    loadingAction.value = ''
  }
}

async function handleDownload(row: SearchRow) {
  try {
    const response = await submitWithDuplicateConfirmation(row.url)
    if (response.code === 200) {
      message.success(`已提交下载：${row.name}`)
      if (response.data?.id) watchTaskCompletion(response.data.id)
    } else if (response.code !== 499) message.error(response.message || '提交失败')
  } catch (error) {
    message.error(error instanceof Error ? `提交失败：${error.message}` : '提交失败')
  }
}
function openSource(row: SearchRow) {
  try {
    const url = new URL(row.url)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new Error()
    window.open(url.href, '_blank', 'noopener,noreferrer')
  } catch { message.error('来源链接无效') }
}
async function copyURL(row: SearchRow) {
  try {
    await navigator.clipboard.writeText(row.url)
    message.success('已复制链接')
  } catch { message.error('复制失败，请从歌曲信息中手动复制链接') }
}

const columns: DataTableColumns<SearchRow> = [
  {
    title: '歌曲', key: 'name', minWidth: 500, ellipsis: { tooltip: true },
    render: (row) => h(NTooltip, { trigger: 'hover', placement: 'top' }, {
      trigger: () => h('span', { class: 'song-title' }, [
        row.name,
        row.is_vip ? h(NTag, { size: 'tiny', type: 'warning', class: 'vip-tag' }, { default: () => 'VIP' }) : null,
      ]),
      default: () => row.url,
    }),
  },
  { title: '歌手 / UP 主', key: 'artists', width: 230, ellipsis: { tooltip: true } },
  { title: '专辑', key: 'album', width: 210, ellipsis: { tooltip: true }, render: (row) => row.album || '-' },
  { title: '时长', key: 'duration', width: 76, render: (row) => formatDuration(row.duration_sec) },
  {
    title: '平台', key: 'platform', width: 100,
    render: (row) => h(NTag, { size: 'small' }, { default: () => platformLabel(row.platform) }),
  },
  {
    title: '操作', key: 'actions', width: 160,
    render: (row) => h(NSpace, { size: 2, wrap: false }, {
      default: () => [
        h(NButton, { size: 'tiny', type: 'primary', quaternary: true, onClick: () => handleDownload(row) }, { default: () => '下载' }),
        h(NButton, { size: 'tiny', quaternary: true, title: '在来源平台打开试听', onClick: () => openSource(row) }, { default: () => '试听' }),
        h(NButton, { size: 'tiny', quaternary: true, onClick: () => copyURL(row) }, { default: () => '复制' }),
      ],
    }),
  },
]

const platformConfigPromise = get('/system/config')
  .then((res) => {
    const config = res.data as any
    qqEnabled.value = !!config?.config?.qq_music_api?.enable
    if (!qqEnabled.value) selectedPlatforms.value = selectedPlatforms.value.filter((platform) => platform !== 'qq')
  })
  .catch(() => { qqEnabled.value = false; selectedPlatforms.value = selectedPlatforms.value.filter((platform) => platform !== 'qq') })
  .finally(() => { platformConfigReady.value = true })

onMounted(() => {
  loadHistory()
  void platformConfigPromise
})
</script>

<style scoped>
.search-page { max-width: 1440px; margin: 0 auto; }
.page-heading { display: flex; align-items: center; margin: 0 0 12px; }
.page-heading :deep(.n-h2) { margin: 0; }

.search-toolbar {
  display: flex;
  align-items: center;
  gap: 14px;
  min-height: 60px;
  box-sizing: border-box;
  padding: 7px 10px;
  border: 1px solid rgba(145,160,154,.13);
  border-radius: 10px;
  background: linear-gradient(105deg, rgba(25,30,36,.9), rgba(20,24,29,.76));
}
.keyword-group { width: min(100%, 460px); flex: 0 1 460px; }
.platform-selectors { display: flex; align-items: center; gap: 12px; white-space: nowrap; }
.platform-actions { display: flex; align-items: center; padding-left: 8px; border-left: 1px solid rgba(145,160,154,.16); }

.result-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 42px;
  box-sizing: border-box;
  padding: 5px 8px;
  border: 1px solid rgba(145,160,154,.1);
  border-radius: 8px;
  background: rgba(18,21,26,.62);
}
.filter-controls { display: flex; align-items: center; gap: 8px; min-width: 0; }
.filter-query { width: 240px; }
.filter-platform { width: 132px; }
.filter-vip { width: 118px; }

.pagination-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  min-height: 42px;
  padding: 4px 2px;
  border-top: 1px solid rgba(145,160,154,.14);
}
.pagination-summary { color: #c1cbc6; font-size: 11px; font-variant-numeric: tabular-nums; }
.search-page :deep(.n-data-table-td) { padding: 8px 10px; }
.search-page :deep(.n-data-table-th) { padding: 10px; }
.summary-muted { color: #74817c; }
.pagination-controls { display: flex; align-items: center; gap: 8px; flex-wrap: nowrap; }

.history-strip {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 34px;
  padding-top: 4px;
  border-top: 1px solid rgba(145,160,154,.1);
}
.history-label { flex: 0 0 auto; color: #87938e; font-size: 11px; }
.history-items { display: flex; flex: 1 1 auto; align-items: center; gap: 6px; min-width: 0; overflow-x: auto; scrollbar-width: thin; }
.history-tag { flex: 0 0 auto; cursor: pointer; }
.song-title { cursor: help; font-weight: 500; }
.vip-tag { margin-left: 6px; }

@media (max-width: 1100px) {
  .search-toolbar { gap: 10px; flex-wrap: wrap; }
  .keyword-group { flex: 1 1 100%; width: 100%; max-width: 560px; }
}
@media (max-width: 760px) {
  .platform-selectors { width: 100%; flex-wrap: wrap; gap: 8px 12px; }
  .platform-actions { margin-left: auto; }
  .result-toolbar { align-items: stretch; flex-direction: column; }
  .filter-controls { width: 100%; flex-wrap: wrap; }
  .filter-query, .filter-platform, .filter-vip { width: 100%; }
  .pagination-bar { align-items: center; flex-wrap: wrap; }
  .pagination-controls { width: 100%; justify-content: space-between; }
  .history-strip { gap: 6px; }
  .history-clear { padding-inline: 4px; }
}
</style>