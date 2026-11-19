<template>
  <section class="search-page">
    <PageHeading eyebrow="MUSIC / DISCOVERY" title="搜索" />

    <div class="search-stack">
      <div class="search-toolbar">
        <n-input-group class="keyword-group">
          <n-input
            v-model:value="keyword"
            placeholder="歌名、歌手，或一小段关键词"
            size="large"
            @keyup.enter="startSearch"
          />
          <n-button type="primary" size="large" :loading="loadingAction === 'search'" :disabled="searching" @click="startSearch">搜索</n-button>
        </n-input-group>
        <div class="platform-row" role="group" aria-label="平台">
          <button
            v-for="platform in platformOrder"
            :key="platform"
            type="button"
            class="platform-chip"
            :class="{ active: selectedPlatforms.includes(platform) }"
            :disabled="searching || (platform === 'qq' && !qqEnabled)"
            @click="togglePlatform(platform)"
          >{{ platformLabel(platform) }}</button>
        </div>
      </div>

      <div v-if="searchHistory.length" class="history-strip">
        <span class="history-label">最近</span>
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

      <div v-if="searchPlatforms.length && (searching || activeErrors.length)" class="search-progress" aria-live="polite">
        <span
          v-for="platform in searchPlatforms"
          :key="platform"
          class="progress-chip"
          :data-status="platformProgress[platform]?.status || 'searching'"
        >{{ platformLabel(platform) }} · {{ progressLabel(platformProgress[platform]) }}</span>
      </div>

      <n-alert v-if="activeErrors.length" type="warning" :show-icon="false">
        部分平台没有返回：{{ activeErrors.join('；') }}。其余结果仍然可用。
      </n-alert>
      <n-alert v-if="truncated" type="warning" :show-icon="false">
        有的平台扫到了上限，这一页可能不是全部。
      </n-alert>

      <div v-if="searched && lastSearchedKeyword && !searchDefinitionDirty" class="result-toolbar">
        <n-input v-model:value="resultQuery" clearable size="small" placeholder="在结果里再筛歌名、歌手或专辑" class="filter-query" />
        <n-select v-model:value="resultPlatform" size="small" :options="resultPlatformOptions" class="filter-platform" />
        <n-select v-model:value="vipFilter" size="small" :options="vipOptions" class="filter-vip" />
      </div>

      <div v-if="searching && results.length === 0" class="result-list" aria-hidden="true">
        <div v-for="n in 4" :key="n" class="result-row skeleton">
          <span class="skel skel-title" />
          <span class="skel skel-meta" />
        </div>
      </div>

      <div v-else-if="results.length" class="result-list" :class="{ dimmed: searching }">
        <article v-for="row in results" :key="rowKey(row)" class="result-row">
          <div class="result-copy">
            <div class="result-title">
              <span>{{ row.name }}</span>
              <em v-if="row.is_vip">VIP</em>
            </div>
            <p>{{ row.artists || '未知歌手' }}<template v-if="row.album"> · {{ row.album }}</template> · {{ formatDuration(row.duration_sec) }}</p>
          </div>
          <span class="source-pill">{{ platformLabel(row.platform) }}</span>
          <div class="result-actions">
            <n-button size="small" type="primary" secondary :loading="!!downloading[row.url]" :disabled="!!downloading[row.url]" @click="handleDownload(row)">下载</n-button>
            <n-button size="small" quaternary @click="openSource(row)">试听</n-button>
            <n-button size="small" quaternary @click="copyURL(row)">复制</n-button>
          </div>
        </article>
      </div>

      <div v-if="searched && !searching && results.length === 0" class="empty-well">
        没有找到「{{ lastSearchedKeyword }}」。换个说法，或少选一个平台再试。
      </div>

      <div v-if="searched" class="pagination-bar">
        <span class="pagination-summary">第 {{ currentPage }} 页 · 本页 {{ results.length }} 首</span>
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
          <n-button secondary :disabled="currentPage <= 1 || searching || searchRequestDirty" :loading="loadingAction === 'previous'" @click="goToPage(currentPage - 1)">上一页</n-button>
          <n-button secondary :disabled="!hasMore || searching || searchRequestDirty || currentPage * batchSize > 2000" :loading="loadingAction === 'next'" @click="goToPage(currentPage + 1)">下一页</n-button>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { NInputGroup, NInput, NButton, NAlert, NSelect, useMessage } from 'naive-ui'
import { get } from '../api/http'
import { streamSearch } from '../api/search'
import type { SearchCompleteEvent, SearchPlatformEvent } from '../api/search'
import PageHeading from '../components/PageHeading.vue'
import { watchTaskCompletion } from '../components/taskNotifications'
import { useTaskSubmission } from '../components/useTaskSubmission'
import type { SearchResultItem } from '../types/search'

defineOptions({ name: 'Search' })

type SearchRow = SearchResultItem
type LiveProgress = { status: SearchPlatformEvent['status']; count: number; error?: string }

const HISTORY_KEY = 'gymdl_search_history'
const platformOrder = ['netease', 'qq', 'youtube', 'bilibili']
const filterDebounceMs = 400
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
const platformProgress = ref<Record<string, LiveProgress>>({})
let activeSearchController: AbortController | null = null
let liveFrame = 0
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
  platformOrder.filter((platform) => lastSearchedPlatforms.value.includes(platform)),
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
const downloading = ref<Record<string, boolean>>({})
let suppressFilterWatch = false
let pendingRefilter = false
const chosenPlatforms = computed(() => platformOrder.filter((platform) => selectedPlatforms.value.includes(platform) && (platform !== 'qq' || qqEnabled.value)))
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
let filterDebounceTimer: ReturnType<typeof setTimeout> | undefined

function progressLabel(progress?: LiveProgress): string {
  if (!progress || progress.status === 'searching') return '搜索中'
  if (progress.status === 'error') return '失败'
  if (progress.status === 'partial') return `已返回 ${progress.count} 首`
  return `${progress.count} 首`
}
function cancelLiveFrame() {
  if (!liveFrame) return
  cancelAnimationFrame(liveFrame)
  liveFrame = 0
}
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
  if (suppressFilterWatch) return
  if (searching.value) {
    pendingRefilter = true
    return
  }
  clearFilterDebounce()
  if (!canApplyResultFilters()) return
  filterDebounceTimer = setTimeout(applyResultFilters, filterDebounceMs)
})
watch([resultPlatform, vipFilter], () => {
  if (suppressFilterWatch) return
  if (searching.value) {
    pendingRefilter = true
    return
  }
  applyResultFilters()
})

function rowKey(row: SearchResultItem) { return `${row.platform}:${row.song_id}` }
function platformLabel(platform: string): string {
  return ({ netease: '网易云', qq: 'QQ 音乐', youtube: 'YouTube', bilibili: 'B 站' } as Record<string, string>)[platform] || platform
}
function formatDuration(sec: number): string {
  if (!sec) return '时长未知'
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
}
function togglePlatform(platform: string) {
  if (searching.value || (platform === 'qq' && !qqEnabled.value)) return
  selectedPlatforms.value = selectedPlatforms.value.includes(platform)
    ? selectedPlatforms.value.filter((item) => item !== platform)
    : [...selectedPlatforms.value, platform]
}
function loadHistory() {
  try { searchHistory.value = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]') } catch { /* ignore */ }
}
function saveHistory(query: string) {
  const next = [query, ...searchHistory.value.filter((item) => item !== query)].slice(0, 8)
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

  suppressFilterWatch = true
  if (query !== lastSearchedKeyword.value) {
    resultQuery.value = ''
    resultPlatform.value = 'all'
    vipFilter.value = 'all'
  } else if (resultPlatform.value !== 'all' && !platforms.includes(resultPlatform.value)) {
    resultPlatform.value = 'all'
  }
  await nextTick()
  suppressFilterWatch = false
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
  const previousState = action === 'search' ? null : {
    results: [...results.value],
    currentPage: currentPage.value,
    hasMore: hasMore.value,
    truncated: truncated.value,
    errors: { ...errors.value },
    progress: { ...platformProgress.value },
  }
  activeSearchController?.abort()
  cancelLiveFrame()
  const controller = new AbortController()
  activeSearchController = controller
  searching.value = true
  loadingAction.value = action
  const progressPlatforms = resultPlatform.value === 'all' ? platforms : [resultPlatform.value]
  searchPlatforms.value = [...progressPlatforms]
  platformProgress.value = Object.fromEntries(progressPlatforms.map((platform) => [platform, { status: 'searching' as const, count: 0 }]))
  errors.value = {}
  truncated.value = false
  if (action === 'search') {
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
  const liveBuckets: Record<string, SearchRow[]> = {}
  const publishLive = () => {
    const lists = progressPlatforms.map((platform) => liveBuckets[platform] || [])
    const maxItems = lists.reduce((max, items) => Math.max(max, items.length), 0)
    const merged: SearchRow[] = []
    for (let index = 0; index < maxItems; index++) {
      for (const items of lists) {
        if (index < items.length) merged.push(items[index])
      }
    }
    results.value = merged.slice(offset, offset + batchSize.value)
  }
  const scheduleLive = () => {
    if (liveFrame || receivedComplete) return
    liveFrame = requestAnimationFrame(() => {
      liveFrame = 0
      if (receivedComplete || controller.signal.aborted || activeSearchController !== controller) return
      publishLive()
    })
  }
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
        if (controller.signal.aborted || activeSearchController !== controller) return
        const previous = liveBuckets[event.platform] || []
        const items = event.status === 'partial' ? [...previous, ...(event.items || [])] : (event.items || [])
        liveBuckets[event.platform] = items
        platformProgress.value = {
          ...platformProgress.value,
          [event.platform]: { status: event.status, count: items.length, error: event.error },
        }
        if (event.error) errors.value = { ...errors.value, [event.platform]: event.error }
        scheduleLive()
      },
      (complete: SearchCompleteEvent) => {
        if (controller.signal.aborted || activeSearchController !== controller) return
        receivedComplete = true
        cancelLiveFrame()
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
        if (page === 1 && action === 'search') saveHistory(query)
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
      if (pendingRefilter) {
        pendingRefilter = false
        const filtersDiffer = resultQuery.value.trim() !== lastSearchedFilterQuery.value
          || resultPlatform.value !== lastSearchedFilterPlatform.value
          || vipFilter.value !== lastSearchedVipFilter.value
        if (filtersDiffer && canApplyResultFilters()) applyResultFilters()
      }
    }
  }
  return receivedComplete
}

async function handleDownload(row: SearchRow) {
  if (downloading.value[row.url]) return
  downloading.value = { ...downloading.value, [row.url]: true }
  try {
    const response = await submitWithDuplicateConfirmation(row.url)
    if (response.code === 200) {
      message.success(`已提交下载：${row.name}`)
      if (response.data?.id) watchTaskCompletion(response.data.id)
    } else if (response.code !== 499) message.error(response.message || '提交失败')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '提交失败')
  } finally {
    const next = { ...downloading.value }
    delete next[row.url]
    downloading.value = next
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
  } catch { message.error('复制失败') }
}

const platformConfigPromise = get('/system/config')
  .then((res) => {
    const config = res.data as { config?: { qq_music_api?: { enable?: boolean } } }
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
  cancelLiveFrame()
  activeSearchController?.abort()
})
</script>

<style scoped>
.search-page { max-width: 1080px; margin: 0 auto; }
.search-stack { display: flex; flex-direction: column; gap: 12px; }
.search-toolbar {
  display: flex; align-items: center; gap: 14px; min-height: 64px; box-sizing: border-box;
  padding: 8px 10px; border: 1px solid rgba(186,206,198,.16); border-radius: 18px;
  background: rgba(16, 22, 26, .5); box-shadow: inset 0 1px 0 rgba(255,255,255,.05);
}
.search-toolbar:focus-within { border-color: rgba(99,226,183,.4); box-shadow: 0 0 0 3px rgba(99,226,183,.1), inset 0 1px 0 rgba(255,255,255,.05); }
.keyword-group { width: min(100%, 520px); flex: 1 1 360px; }
.platform-row { display: flex; flex-wrap: wrap; gap: 6px; }
.platform-chip, .history-tag {
  min-height: 30px; padding: 4px 12px; border: 1px solid rgba(171,190,184,.18); border-radius: 999px;
  color: #c5d0cb; background: transparent; font: inherit; font-size: 12px; cursor: pointer;
}
.platform-chip.active { color: #06140f; border-color: #63e2b7; background: #63e2b7; }
.platform-chip:disabled, .history-tag:disabled { opacity: .45; cursor: not-allowed; }
.history-strip { display: flex; align-items: center; gap: 8px; min-width: 0; }
.history-label { flex: 0 0 auto; color: #7f8c86; font-size: 12px; }
.history-items { display: flex; gap: 6px; min-width: 0; overflow-x: auto; }
.history-tag { flex: 0 0 auto; background: rgba(16,22,26,.35); }
.history-tag:hover:not(:disabled) { border-color: rgba(99,226,183,.48); color: #8de8c8; }
.search-progress { display: flex; flex-wrap: wrap; gap: 6px; }
.progress-chip { padding: 3px 8px; border-radius: 999px; color: #9aaba4; background: rgba(255,255,255,.04); font-size: 12px; }
.progress-chip[data-status="complete"] { color: #8de8c8; }
.progress-chip[data-status="error"] { color: #e7b3a2; }
.result-toolbar { display: flex; gap: 8px; align-items: center; }
.filter-query { width: min(100%, 280px); }
.filter-platform { width: 132px; }
.filter-vip { width: 118px; }
.result-list { display: flex; flex-direction: column; gap: 8px; }
.result-list.dimmed { opacity: .62; }
.result-row {
  display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: 12px; align-items: center;
  padding: 12px 14px; border: 1px solid rgba(125,150,141,.16); border-radius: 16px;
  background: rgba(16, 22, 26, .42);
}
.result-title { display: flex; align-items: center; gap: 8px; color: #edf7f3; font-weight: 620; }
.result-title em { padding: 1px 6px; border-radius: 999px; color: #3a2a12; background: #e3c07a; font-size: 10px; font-style: normal; font-weight: 700; }
.result-copy p { margin: 4px 0 0; color: #8ea39b; font-size: 13px; }
.source-pill { color: #9aaba4; font-size: 12px; white-space: nowrap; }
.result-actions { display: flex; gap: 6px; }
.skeleton { display: block; }
.skel { display: block; height: 12px; border-radius: 999px; background: rgba(255,255,255,.06); }
.skel-title { width: 42%; margin-bottom: 8px; }
.skel-meta { width: 68%; }
.empty-well { padding: 28px 16px; border: 1px dashed rgba(125,150,141,.24); border-radius: 16px; color: #8ea39b; text-align: center; }
.pagination-bar { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.pagination-summary { color: #8ea39b; font-size: 12px; font-variant-numeric: tabular-nums; }
.pagination-controls { display: flex; align-items: center; gap: 8px; }
.batch-select { width: 124px; }
@media (max-width: 800px) {
  .search-toolbar, .result-row, .result-toolbar, .pagination-bar { align-items: stretch; flex-direction: column; }
  .search-toolbar { display: flex; }
  .result-row { grid-template-columns: 1fr; }
  .filter-query, .filter-platform, .filter-vip, .keyword-group { width: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  .result-list.dimmed { opacity: 1; }
}
</style>
