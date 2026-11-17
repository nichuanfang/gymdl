<template>
  <section class="studio-page">
    <PageHeading eyebrow="LIBRARY / DUPLICATE REVIEW" title="重复歌曲">
      <template #actions>
        <n-button type="primary" :loading="starting || running" :disabled="running" @click="scan">
          {{ running ? '查重进行中' : '开始查重' }}
        </n-button>
      </template>
    </PageHeading>

    <n-alert type="info" :show-icon="false">只扫描、只分组，不删除文件，也不改变入库和找回遗珠的判断。</n-alert>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>

    <n-card size="small" :bordered="false" class="studio-card">
      <p class="status-line">{{ statusText }}</p>
      <p v-if="report.skipped" class="studio-help">有 {{ report.skipped }} 个文件没能算出指纹，已跳过。</p>
      <p v-if="report.updated_at && report.status !== 'idle'" class="studio-help">更新于 {{ formatTime(report.updated_at) }}</p>
      <div class="count-row">
        <span>重复 {{ sameGroups.length }} 组</span>
        <span>相似 {{ similarGroups.length }} 组</span>
      </div>
    </n-card>

    <article v-for="(group, index) in report.groups" :key="`${group.kind}-${index}`" class="group-card">
      <header>
        <n-tag :bordered="false" :type="group.kind === 'same' ? 'success' : 'warning'">
          {{ group.kind === 'same' ? '重复' : '相似' }}
        </n-tag>
        <span>{{ group.kind === 'same' ? '同一段录音的不同文件' : '听起来接近，但没有并成重复' }}</span>
      </header>
      <ul>
        <li v-for="track in group.tracks" :key="track.path">
          <div>
            <strong>{{ track.name || track.path }}</strong>
            <p>{{ track.path }}</p>
          </div>
          <a :href="streamUrl(track.path)" target="_blank" rel="noopener noreferrer">试听</a>
        </li>
      </ul>
    </article>

    <n-empty v-if="!running && report.status === 'done' && !report.groups.length" description="这次没有发现重复或相似的歌曲" />
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NTag, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { getFileStreamUrl, getLibraryDuplicates, startLibraryDuplicateScan, type LibraryDuplicateReport } from '../api'

defineOptions({ name: 'Duplicates' })

const message = useMessage()
const starting = ref(false)
const error = ref('')
const report = ref<LibraryDuplicateReport>({ status: 'idle', scanned: 0, total: 0, skipped: 0, groups: [] })
let pollTimer: ReturnType<typeof setTimeout> | null = null
let disposed = false
let pageActive = true
let skipActivate = true

const running = computed(() => report.value.status === 'running')
const sameGroups = computed(() => (report.value.groups || []).filter((group) => group.kind === 'same'))
const similarGroups = computed(() => (report.value.groups || []).filter((group) => group.kind === 'similar'))
const statusText = computed(() => {
  const current = report.value
  if (current.status === 'running') return `正在比对 ${current.scanned} / ${current.total || '…'}`
  if (current.status === 'done') return `已完成，读过 ${current.scanned} 个文件`
  if (current.status === 'error') return current.error || '查重没有完成'
  return '还没有查过这套曲库'
})

function streamUrl(path: string) {
  return getFileStreamUrl(path)
}
function formatTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}
function stopPoll() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}
function schedule() {
  stopPoll()
  if (disposed || !pageActive || report.value.status !== 'running') return
  pollTimer = setTimeout(() => { void refresh() }, 2000)
}

async function refresh() {
  try {
    const response = await getLibraryDuplicates()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '读取查重结果失败')
    report.value = { ...response.data, groups: response.data.groups || [] }
    error.value = report.value.status === 'error' ? (report.value.error || '查重没有完成') : ''
  } catch (err) {
    error.value = err instanceof Error ? err.message : '读取查重结果失败'
  } finally {
    schedule()
  }
}

async function scan() {
  if (running.value || starting.value) return
  starting.value = true
  error.value = ''
  try {
    const response = await startLibraryDuplicateScan()
    if (response.code === 200 && response.data) {
      report.value = { ...response.data, groups: response.data.groups || [] }
      schedule()
      return
    }
    if (response.code === 409) {
      message.info(response.message || '已有一次查重在进行')
      await refresh()
      return
    }
    throw new Error(response.message || '没有开始查重')
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有开始查重')
  } finally {
    starting.value = false
  }
}

onMounted(() => {
  skipActivate = true
  void refresh()
})
onActivated(() => {
  pageActive = true
  if (skipActivate) {
    skipActivate = false
    return
  }
  void refresh()
})
onDeactivated(() => {
  pageActive = false
  stopPoll()
})
onUnmounted(() => {
  disposed = true
  stopPoll()
})
</script>

<style scoped>
.studio-page { max-width: 980px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.studio-card, .group-card {
  background: rgba(16, 22, 26, .42); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px);
}
.group-card { padding: 14px 16px; border: 1px solid rgba(125, 150, 141, .16); border-radius: 16px; }
.group-card header, .count-row, .group-card li { display: flex; justify-content: space-between; gap: 12px; align-items: center; }
.group-card header span, .studio-help, .group-card p { color: #7d8c86; font-size: 12px; }
.status-line { margin: 0; font-size: 16px; font-weight: 600; }
.studio-help { margin: 6px 0 0; }
.count-row { margin-top: 10px; color: #b7c7c0; }
.group-card ul { margin: 10px 0 0; padding: 0; list-style: none; }
.group-card li { padding: 8px 0; border-top: 1px solid rgba(125, 150, 141, .14); }
.group-card strong { display: block; }
.group-card p { margin: 3px 0 0; overflow-wrap: anywhere; }
.group-card a { color: #63e2b7; text-decoration: none; flex-shrink: 0; }
@media (max-width: 700px) {
  .group-card li { align-items: flex-start; }
}
</style>
