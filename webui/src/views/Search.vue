<template>
  <div>
    <n-h2>搜索</n-h2>
    <n-space vertical :size="16" :wrap-item="false">
      <!-- 搜索栏 -->
      <n-space align="center" :size="8">
        <n-input-group>
          <n-input
            v-model:value="keyword"
            placeholder="输入歌曲 / 歌手 / 视频关键词"
            size="large"
            style="width: 320px"
            @focus="selectKeyword"
            @keyup.enter="doSearchImmediate(0)"
          />
          <n-button type="primary" size="large" :disabled="searching" @click="doSearchImmediate(0)">
            {{ searching ? '搜索中' : '搜索' }}
          </n-button>
        </n-input-group>
        <n-checkbox-group v-model:value="selectedPlatforms">
          <n-checkbox value="netease" label="网易云" />
          <n-checkbox value="qq" label="QQ 音乐" :disabled="!qqEnabled" />
          <n-checkbox value="youtube" label="YouTube" />
          <n-checkbox value="bilibili" label="B 站" />
        </n-checkbox-group>
        <n-button quaternary size="small" @click="togglePlatforms">{{ allPlatformsSelected ? '反选' : '全选' }}</n-button>
      </n-space>

      <n-space v-if="searched && results.length" align="center" :size="10" class="result-filters">
        <n-input v-model:value="resultQuery" clearable size="small" placeholder="筛选歌曲 / 歌手 / 专辑" style="width: 250px" />
        <n-select v-model:value="resultPlatform" size="small" :options="resultPlatformOptions" style="width: 140px" />
        <n-select v-model:value="vipFilter" size="small" :options="vipOptions" style="width: 120px" />
        <n-text depth="3" style="font-size: 12px">显示 {{ filteredResults.length }} / {{ results.length }} 条</n-text>
      </n-space>

      <!-- 错误提示 -->
      <n-alert v-if="platformErrors" type="warning" closable @close="errors = {}">
        部分平台搜索失败：{{ platformErrors }}
      </n-alert>

      <!-- 结果表格 -->
      <n-data-table
        :columns="columns"
        :data="filteredResults"
        :loading="searching"
        :bordered="false"
        :row-key="(row: SearchResultItem) => row.platform + row.song_id"
        :max-height="600"
        :scroll-x="900"
      />

      <!-- 分页 -->
      <n-space justify="end" v-if="results.length > 0">
        <n-button size="small" :disabled="offset <= 0" @click="doSearchImmediate(offset - 20)">上一页</n-button>
        <n-text depth="3" style="font-size: 12px; align-self: center">
          第 {{ Math.floor(offset / 20) + 1 }} 批 · 当前显示 {{ filteredResults.length }} / {{ results.length }} 条
        </n-text>
        <n-button size="small" :disabled="!hasMore" @click="doSearchImmediate(offset + 20)">下一页</n-button>
      </n-space>

      <n-empty v-if="!searching && searched && results.length === 0" description="没有找到相关结果" style="margin-top: 40px" />
      <n-empty v-else-if="!searching && searched && results.length > 0 && filteredResults.length === 0" description="当前筛选条件下没有匹配结果" style="margin-top: 24px" />

      <!-- 搜索历史 -->
      <n-card v-if="searchHistory.length > 0" size="small">
        <template #header>
          <n-space justify="space-between" align="center">
            <span>最近搜索</span>
            <n-button quaternary size="tiny" @click="clearHistory">清空</n-button>
          </n-space>
        </template>
        <n-space :size="6">
          <n-tag
            v-for="k in searchHistory"
            :key="k"
            size="small"
            style="cursor: pointer"
            @click="keyword = k; doSearchImmediate(0)"
          >
            {{ k }}
          </n-tag>
        </n-space>
      </n-card>
    </n-space>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import {
  NH2, NInputGroup, NInput, NButton, NSpace, NTag, NDataTable, NAlert,
  NEmpty, NText, NCheckbox, NCheckboxGroup, NCard, NTooltip, NSelect, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { get } from '../api/http'
import { submitTask } from '../api'
import { watchTaskCompletion } from '../components/taskNotifications'
import type { SearchResultItem } from '../types/search'

defineOptions({ name: 'Search' })

const HISTORY_KEY = 'gymdl_search_history'
const message = useMessage()
const keyword = ref('')
const lastSearchedKeyword = ref('')
const searching = ref(false)
const searched = ref(false)
const results = ref<SearchResultItem[]>([])
const errors = ref<Record<string, string>>({})
const offset = ref(0)
const hasMore = ref(false)
const qqEnabled = ref(true)
const platformConfigReady = ref(false)
const searchHistory = ref<string[]>([])
const resultQuery = ref('')
const resultPlatform = ref('all')
const vipFilter = ref('all')
const resultPlatformOptions = computed(() => [
  { label: '全部平台', value: 'all' },
  { label: '网易云', value: 'netease' },
  { label: 'QQ 音乐', value: 'qq', disabled: !qqEnabled.value },
  { label: 'YouTube', value: 'youtube' },
  { label: 'B 站', value: 'bilibili' },
])
const vipOptions = [
  { label: '全部结果', value: 'all' },
  { label: '仅免费', value: 'free' },
  { label: '仅 VIP', value: 'vip' },
]
const filteredResults = computed(() => {
  const query = resultQuery.value.trim().toLocaleLowerCase()
  return results.value.filter((item) => {
    const textMatches = !query || [item.name, item.artists, item.album, item.url].some((field) => field?.toLocaleLowerCase().includes(query))
    const platformMatches = resultPlatform.value === 'all' || item.platform === resultPlatform.value
    const vipMatches = vipFilter.value === 'all' || (vipFilter.value === 'vip' ? item.is_vip : !item.is_vip)
    return textMatches && platformMatches && vipMatches
  })
})

const selectedPlatforms = ref<string[]>(['netease', 'qq', 'youtube', 'bilibili'])
const allPlatformsSelected = computed(() => {
  const enabled = enabledPlatforms()
  return enabled.length > 0 && enabled.every((platform) => selectedPlatforms.value.includes(platform))
})

const platformErrors = computed(() => {
  const entries = Object.entries(errors.value)
  if (entries.length === 0) return ''
  return entries.map(([p, e]) => `${p}: ${e}`).join('；')
})

function platformLabel(p: string): string {
  const map: Record<string, string> = {
    netease: '网易云', qq: 'QQ', youtube: 'YouTube', bilibili: 'B 站',
  }
  return map[p] || p
}

function formatDuration(sec: number): string {
  if (!sec) return '-'
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

function enabledPlatforms() {
  return ['netease', 'qq', 'youtube', 'bilibili'].filter((platform) => platform !== 'qq' || qqEnabled.value)
}

function togglePlatforms() {
  selectedPlatforms.value = allPlatformsSelected.value ? [] : enabledPlatforms()
}

function selectKeyword(event: FocusEvent) {
  const input = event.target
  if (input instanceof HTMLInputElement) input.select()
}

function loadHistory() {
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    if (raw) searchHistory.value = JSON.parse(raw)
  } catch { /* ignore */ }
}

function saveHistory(kw: string) {
  const list = [kw, ...searchHistory.value.filter((k) => k !== kw)].slice(0, 10)
  searchHistory.value = list
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(list)) } catch { /* ignore */ }
}

function clearHistory() {
  searchHistory.value = []
  localStorage.removeItem(HISTORY_KEY)
}

async function doSearchImmediate(newOffset: number) {
  if (searching.value) return
  if (!keyword.value.trim()) {
    message.warning('请输入关键词')
    return
  }

  searching.value = true
  try {
    // 平台配置在后台异步读取，但搜索前等待它完成，避免 QQ 尚未确认启用时误发请求。
    if (!platformConfigReady.value) await platformConfigPromise
    if (selectedPlatforms.value.length === 0) {
      message.warning('请至少选择一个平台')
      return
    }

    const query = keyword.value.trim()
    if (newOffset === 0 && query !== lastSearchedKeyword.value) {
      resultQuery.value = ''
      resultPlatform.value = 'all'
      vipFilter.value = 'all'
      lastSearchedKeyword.value = query
    }
    searched.value = true
    offset.value = newOffset
    const res = await get<{ items: SearchResultItem[]; total: number; has_more: boolean; errors: Record<string, string>; keyword: string }>(
      '/search',
      {
        keyword: query,
        platform: selectedPlatforms.value.join(','),
        offset: newOffset,
        limit: 20,
      },
    )
    if (res.code === 200 && res.data) {
      results.value = res.data.items
      hasMore.value = !!res.data.has_more
      errors.value = res.data.errors || {}
      saveHistory(query)
    } else {
      message.error(res.message || '搜索失败')
      results.value = []
      hasMore.value = false
      errors.value = {}
    }
  } catch (error) {
    message.error(error instanceof Error ? `搜索失败：${error.message}` : '搜索失败')
    results.value = []
    hasMore.value = false
    errors.value = {}
  } finally {
    searching.value = false
  }
}

async function handleDownload(row: SearchResultItem) {
  try {
    const res = await submitTask(row.url)
    if (res.code === 200) {
      message.success(`已提交下载：${row.name}`)
      if (res.data?.id) watchTaskCompletion(res.data.id)
    } else {
      message.error(res.message || '提交失败')
    }
  } catch (error) {
    message.error(error instanceof Error ? `提交失败：${error.message}` : '提交失败')
  }
}

function openSource(row: SearchResultItem) {
  const url = new URL(row.url)
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    message.error('试听链接无效')
    return
  }
  window.open(url.href, '_blank', 'noopener,noreferrer')
}

async function copyURL(row: SearchResultItem) {
  try {
    await navigator.clipboard.writeText(row.url)
    message.success('已复制链接')
  } catch {
    message.error('复制失败，请从歌曲信息中手动复制链接')
  }
}

const columns: DataTableColumns<SearchResultItem> = [
  {
    title: '歌曲',
    key: 'name',
    ellipsis: { tooltip: true },
    render: (row) =>
      h(NTooltip, { trigger: 'hover', placement: 'top' }, {
        trigger: () =>
          h('span', { style: 'cursor: help' }, [
            h('span', { style: 'font-weight: 500' }, row.name),
            row.is_vip
              ? h(NTag, { size: 'tiny', type: 'warning', style: 'margin-left: 6px' }, { default: () => 'VIP' })
              : null,
          ]),
        default: () => row.url,
      }),
  },
  { title: '歌手 / UP 主', key: 'artists', width: 160, ellipsis: { tooltip: true } },
  { title: '专辑', key: 'album', width: 150, ellipsis: { tooltip: true }, render: (row) => row.album || '-' },
  { title: '时长', key: 'duration', width: 80, render: (row) => formatDuration(row.duration_sec) },
  {
    title: '平台',
    key: 'platform',
    width: 90,
    render: (row) => h(NTag, { size: 'small' }, { default: () => platformLabel(row.platform) }),
  },
  {
    title: '操作',
    key: 'actions',
    width: 205,
    render: (row) =>
      h(NSpace, { size: 4 }, {
        default: () => [
          h(NButton, { size: 'tiny', type: 'primary', quaternary: true, onClick: () => handleDownload(row) }, { default: () => '下载' }),
          h(NButton, { size: 'tiny', quaternary: true, title: '在来源平台打开试听', onClick: () => openSource(row) }, { default: () => '平台试听' }),
          h(NButton, { size: 'tiny', quaternary: true, onClick: () => copyURL(row) }, { default: () => '复制链接' }),
        ],
      }),
  },
]

const platformConfigPromise = get('/system/config')
  .then((res) => {
    const cfg = res.data as any
    qqEnabled.value = !!cfg?.config?.qq_music_api?.enable
    if (!qqEnabled.value) {
      selectedPlatforms.value = selectedPlatforms.value.filter((platform) => platform !== 'qq')
    }
  })
  .catch(() => {
    // 配置不可用时对 QQ 失败关闭，其他平台仍可使用。
    qqEnabled.value = false
    selectedPlatforms.value = selectedPlatforms.value.filter((platform) => platform !== 'qq')
  })
  .finally(() => {
    platformConfigReady.value = true
  })

onMounted(loadHistory)
</script>

<style scoped>
.result-filters { padding: 10px 12px; border: 1px solid rgba(255,255,255,.07); border-radius: 10px; background: rgba(255,255,255,.02); }
</style>
