<template>
  <section class="search-page">
    <PageHeading eyebrow="MUSIC / DISCOVERY" title="搜索" />

    <n-space vertical :size="8" :wrap-item="false">
      <div class="search-toolbar">
        <n-input-group class="keyword-group">
          <n-input
            v-model:value="keyword"
            placeholder="输入歌曲 / 歌手 / 视频关键词"
            size="large"
            :disabled="searching"
            @focus="selectKeyword"
            @keyup.enter="startSearch"
          />
          <n-button type="primary" size="large" :loading="loadingAction === 'search' || loadingAction === 'filter'" :disabled="searching" @click="startSearch">搜索</n-button>
        </n-input-group>
        <n-checkbox-group v-model:value="selectedPlatforms" class="platform-selectors">
          <n-checkbox value="netease" label="网易云" :disabled="searching" />
          <n-checkbox value="qq" label="QQ 音乐" :disabled="searching || !qqEnabled" />
          <n-checkbox value="youtube" label="YouTube" :disabled="searching" />
          <n-checkbox value="bilibili" label="B 站" :disabled="searching" />
        </n-checkbox-group>
        <div class="platform-actions">
          <n-button quaternary size="small" :disabled="searching" @click="togglePlatforms">{{ allPlatformsSelected ? '反选' : '全选' }}</n-button>
        </div>
      </div>

      <div v-if="searching && searchPlatforms.length" class="search-progress" aria-live="polite">
        <span class="progress-label">平台状态</span>
        <n-tag
          v-for="platform in searchPlatforms"
          :key="platform"
          size="small"
          :type="progressTagType(platformProgress[platform]?.status)"
        >
          {{ platformLabel(platform) }} · {{ progressLabel(platformProgress[platform]) }}
        </n-tag>
      </div>

      <n-alert v-if="activeErrors.length" type="warning" :show-icon="false">
        部分平台搜索失败：{{ activeErrors.join('；') }}。已返回其他平台结果。
      </n-alert>
      <n-alert v-if="truncated" type="warning" :show-icon="false">
        部分平台已达到候选扫描上限，筛选结果可能不完整。
      </n-alert>

      <div v-if="searched && lastSearchedKeyword && !searchDefinitionDirty" class="result-toolbar">
        <div class="filter-controls">
          <n-input v-model:value="resultQuery" clearable size="small" placeholder="筛选歌曲 / 歌手 / 专辑" class="filter-query" />
          <n-select v-model:value="resultPlatform" size="small" :options="resultPlatformOptions" class="filter-platform" />
          <n-select v-model:value="vipFilter" size="small" :options="vipOptions" class="filter-vip" />
        </div>
      </div>
      <n-data-table
        :columns="columns"
        :data="results"
        size="small"
        :bordered="false"
        :row-key="rowKey"
        :scroll-x="1164"
      />

      <div v-if="searched" class="pagination-bar">
        <span class="pagination-summary">本页 {{ results.length }} 条</span>
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

      <div v-if="searchHistory.length" class="history-strip">
        <span class="history-label">最近搜索</span>
        <div class="history-items">
          <button
            v-for="item in searchHistory"
            :key="item"
            type="button"
            class="history-tag"
            :disabled="searching"
            @click="keyword = item; startSearch()"
          >{{ item }}</button>
        </div>
        <n-button quaternary size="tiny" class="history-clear" @click="clearHistory">清空</n-button>
      </div>
    </n-space>
  </section>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, h } from 'vue'
import {
  NInputGroup, NInput, NButton, NSpace, NTag, NDataTable, NAlert,
  NEmpty, NCheckbox, NCheckboxGroup, NTooltip, NSelect, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { get } from '../api/http'
import { streamSearch } from '../api/search'
import type { SearchCompleteEvent, SearchPlatformEvent } from '../api/search'
import PageHeading from '../components/PageHeading.vue'
import { watchTaskCompletion } from '../components/taskNotifications'
import { useTaskSubmission } from '../components/useTaskSubmission'
import type { SearchResultItem } from '../types/search'

type SearchRow = SearchResultItem

const HISTORY_KEY = 'gymdl_search_history'
const PLATFORM_ORDER = ['netease', 'qq', 'youtube', 'bilibili']
const filterDebounceMs = 300
const message = useMessage()
const { submitWithDuplicateConfirmation } = useTaskSubmission()
const keyword = ref('')
const lastSearchedKeyword = ref('')
const searching = ref(false)
const loadingAction = ref<'search' | 'filter' | 'previous' | 'next' | 'size' | ''>('')
const searched = ref(false)
const results = ref<SearchRow[]>([])
const errors = ref<Record<string, string>>({})
const searchPlatforms = ref<string[]>([])
const platformProgress = ref<Record<string, SearchPlatformEvent>>({})
let activeSearchController: AbortController | null = null
const hasMore = ref(false)
const truncated = ref(false)
const currentPage = ref(1)
const lastSearchedPlatforms = ref<string[]>([])
const qqEnabled = ref(true)
const platformConfigReady = ref(false)
const searchHistory = ref<string[]>([])
const resultQuery = ref('')
const resultPlatform = ref('all')
const vipFilter = ref('all')
const lastSearchedFilterQuery = ref('')
const lastSearchedFilterPlatform = ref('all')
const lastSearchedVipFilter = ref('all')
const batchSize = ref(10)
const batchOptions = [10, 20, 50, 100].map((value) => ({ label: `每页 ${value} 首`, value }))
const availableResultPlatforms = computed(() =>
  PLATFORM_ORDER.filter((platform) => lastSearchedPlatforms.value.includes(platform)),
)
const resultPlatformOptions = computed(() => [
  { label: '全部平台', value: 'all' },
  ...availableResultPlatforms.value.map((platform) => ({ label: platformLabel(platform), value: platform })),
])
const vipOptions = [
  { label: '全部结果', value: 'all' },
  { label: '仅免费', value: 'free' },
  { label: '仅 VIP', value: 'vip' },
]
const selectedPlatforms = ref<string[]>(['netease', 'qq', 'youtube', 'bilibili'])
const chosenPlatforms = computed(() => PLATFORM_ORDER.filter((platform) => selectedPlatforms.value.includes(platform) && (platform !== 'qq' || qqEnabled.value)))
const searchDefinitionDirty = computed(() =>
  keyword.value.trim() !== lastSearchedKeyword.value || chosenPlatforms.value.join(',') !== lastSearchedPlatforms.value.join(','),
)
const searchRequestDirty = computed(() =>
  searchDefinitionDirty.value ||
  resultQuery.value.trim() !== lastSearchedFilterQuery.value ||
  resultPlatform.value !== lastSearchedFilterPlatform.value ||
  vipFilter.value !== lastSearchedVipFilter.value,
)
const activeErrors = computed(() => Object.entries(errors.value).map(([platform, error]) => `${platformLabel(platform)}：${error}`))
function progressLabel(progress?: SearchPlatformEvent): string {
  if (!progress || progress.status === 'searching') return '搜索中'
  if (progress.status === 'partial') return `已返回 ${progress.items?.length || 0} 首，继续搜索`
  if (progress.status === 'complete') return `完成 ${progress.items?.length || 0} 首`
  return '搜索失败'
}
function progressTagType(status?: SearchPlatformEvent['status']): 'default' | 'info' | 'success' | 'warning' | 'error' {
  if (status === 'complete') return 'success'
  if (status === 'error') return 'error'
  if (status === 'partial') return 'info'
  return 'default'
}
let filterDebounceTimer: ReturnType<typeof setTimeout> | undefined
function clearFilterDebounce() {
  if (filterDebounceTimer !== undefined) {
    clearTimeout(filterDebounceTimer)
    filterDebounceTimer = undefined
  }
}
function canApplyResultFilters() {
  return searched.value && !!lastSearchedKeyword.value && lastSearchedPlatforms.value.length > 0 && !searchDefinitionDirty.value
}
function applyResultFilters() {
  clearFilterDebounce()
  if (!canApplyResultFilters()) return
  void loadPage(1, lastSearchedKeyword.value, lastSearchedPlatforms.value, 'filter')
}
watch(resultQuery, () => {
  clearFilterDebounce()
  if (!canApplyResultFilters()) return
  filterDebounceTimer = setTimeout(applyResultFilters, filterDebounceMs)
})
watch([resultPlatform, vipFilter], applyResultFilters)
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
  clearFilterDebounce()
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
  } else if (resultPlatform.value !== 'all' && !platforms.includes(resultPlatform.value)) {
    resultPlatform.value = 'all'
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
  const previousSize = batchSize.value
  batchSize.value = size
  if (searched.value && lastSearchedKeyword.value && lastSearchedPlatforms.value.length && !searchRequestDirty.value) {
    const loaded = await loadPage(1, lastSearchedKeyword.value, lastSearchedPlatforms.value, 'size')
    if (!loaded) batchSize.value = previousSize
  }
}

async function loadPage(page: number, query: string, platforms: string[], action: 'search' | 'filter' | 'previous' | 'next' | 'size'): Promise<boolean> {
  const previousState = action === 'search' || action === 'filter' ? null : {
    results: [...results.value],
    currentPage: currentPage.value,
    hasMore: hasMore.value,
    truncated: truncated.value,
    errors: { ...errors.value },
    progress: { ...platformProgress.value },
  }
  activeSearchController?.abort()
  const controller = new AbortController()
  activeSearchController = controller
  searching.value = true
  loadingAction.value = action
  const progressPlatforms = resultPlatform.value === 'all' ? platforms : [resultPlatform.value]
  searchPlatforms.value = [...progressPlatforms]
  platformProgress.value = Object.fromEntries(progressPlatforms.map((platform) => [platform, { platform: platform as SearchResultItem['platform'], status: 'searching', items: [] }]))
  errors.value = {}
  truncated.value = false
  if (action === 'search' || action === 'filter') {
    results.value = []
    hasMore.value = false
    currentPage.value = 1
  }
  const offset = (page - 1) * batchSize.value
  const filters = {
    query: resultQuery.value.trim(),
    platform: resultPlatform.value,
    vip: vipFilter.value,
  }
  let receivedComplete = false
  try {
    await streamSearch(
      {
        keyword: query,
        platform: platforms,
        offset,
        limit: batchSize.value,
        filterQuery: filters.query,
        filterPlatform: filters.platform,
        filterVip: filters.vip,
      },
      (event) => {
        const previous = platformProgress.value[event.platform]
        const items = event.status === 'partial'
          ? [...(previous?.items || []), ...(event.items || [])]
          : (event.items || previous?.items || [])
        platformProgress.value = { ...platformProgress.value, [event.platform]: { ...event, items } }
        if (event.error) errors.value = { ...errors.value, [event.platform]: event.error }
      },
      (complete: SearchCompleteEvent) => {
        receivedComplete = true
        results.value = complete.items || []
        hasMore.value = !!complete.has_more
        truncated.value = !!complete.truncated
        errors.value = complete.errors || {}
        currentPage.value = page
        lastSearchedKeyword.value = query
        lastSearchedPlatforms.value = [...platforms]
        lastSearchedFilterQuery.value = filters.query
        lastSearchedFilterPlatform.value = filters.platform
        lastSearchedVipFilter.value = filters.vip
        searched.value = true
        if (page === 1) saveHistory(query)
      },
      controller.signal,
    )
  } catch (error) {
    if (previousState && !receivedComplete) {
      results.value = previousState.results
      currentPage.value = previousState.currentPage
      hasMore.value = previousState.hasMore
      truncated.value = previousState.truncated
      errors.value = previousState.errors
      platformProgress.value = previousState.progress
    }
    if (!controller.signal.aborted) message.error(error instanceof Error ? error.message : '搜索失败')
  } finally {
    if (activeSearchController === controller) {
      activeSearchController = null
      searching.value = false
      loadingAction.value = ''
    }
  }
  return receivedComplete
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
onUnmounted(() => {
  clearFilterDebounce()
  activeSearchController?.abort()
})
</script>

<style scoped>
.search-page { max-width: 1440px; margin: 0 auto; }

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

.search-progress { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; min-height: 30px; padding: 2px 2px; }
.progress-label { margin-right: 2px; color: #7f8c86; font-size: 10px; letter-spacing: .06em; }
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
.history-tag { flex: 0 0 auto; padding: 3px 8px; border: 1px solid rgba(171,190,184,.18); border-radius: 3px; color: #c1cbc6; background: transparent; font: inherit; font-size: 11px; line-height: 1.4; cursor: pointer; }
.history-tag:hover:not(:disabled) { border-color: rgba(99,226,183,.48); color: #8de8c8; }
.history-tag:disabled { opacity: .5; cursor: not-allowed; }
.history-tag:focus-visible { outline: 2px solid #63e2b7; outline-offset: 2px; }
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