<template>
  <section class="studio-page">
    <PageHeading eyebrow="DISCOVERY / REDISCOVER" title="找回遗珠" />

    <n-card size="small" :bordered="false" class="studio-card">
      <div class="control-row">
        <n-select v-model:value="platform" :options="platformOptions" class="platform-select" :disabled="searching" />
        <div class="count-row" role="group" aria-label="数量">
          <button
            v-for="count in counts"
            :key="count"
            type="button"
            class="count-chip"
            :class="{ active: limit === count }"
            :disabled="searching"
            @click="limit = count"
          >{{ count }} 首</button>
        </div>
        <n-button type="primary" :loading="searching" :disabled="acting" @click="search">找回</n-button>
      </div>
      <p class="studio-help">从听过和收藏过的歌里，找出曲库还没有的版本。先在这里看，试听后再入库。入库只是排队，这页会跟着显示进度。</p>
    </n-card>

    <n-alert v-if="notice && tracks.length" type="info" :show-icon="false">{{ notice }}</n-alert>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>

    <div v-if="tracks.length" class="result-head">
      <span>{{ tracks.length }} 首可处理</span>
      <n-button size="small" secondary :disabled="!actionable.length || acting" :loading="batching" @click="confirmEnqueue">
        这组入库
      </n-button>
    </div>

    <div v-if="!searched && !searching" class="empty-well">选好平台和数量后点找回。歌曲会列在这里，可以试听，再决定单首或整组入库。</div>
    <div v-else-if="searching" class="result-list" aria-hidden="true">
      <div v-for="n in limit" :key="n" class="track-card skeleton"><span class="skel" /><span class="skel short" /></div>
    </div>

    <article v-for="track in tracks" :key="trackKey(track)" class="track-card">
      <div class="track-copy">
        <span class="track-title">{{ track.title }}</span>
        <p>{{ track.artist || '未知歌手' }}<template v-if="track.album"> · {{ track.album }}</template><template v-if="track.duration"> · {{ formatDuration(track.duration) }}</template></p>
        <p v-if="track.plays || track.last_played" class="track-meta">
          <template v-if="track.plays">听过 {{ track.plays }} 次</template>
          <template v-if="track.plays && track.last_played"> · </template>
          <template v-if="track.last_played">最近 {{ track.last_played }}</template>
        </p>
        <p v-if="noteOf(track)" class="track-note">{{ noteOf(track) }}</p>
      </div>
      <div class="track-actions">
        <n-button v-if="track.url" size="small" quaternary @click="openListen(track.url)">试听</n-button>
        <template v-if="stateOf(track) === 'dismissed'">
          <n-tag :bordered="false" type="default">{{ stateLabel(track) }}</n-tag>
          <n-button size="small" secondary :loading="busy[trackKey(track)] === 'restore'" :disabled="acting" @click="restoreOne(track)">恢复推荐</n-button>
        </template>
        <n-tag v-else-if="stateOf(track)" :bordered="false" :type="tagType(track)">{{ stateLabel(track) }}</n-tag>
        <template v-else>
          <n-button size="small" type="primary" secondary :loading="busy[trackKey(track)] === 'enqueue'" :disabled="acting" @click="enqueueOne(track)">入库</n-button>
          <n-button size="small" quaternary :loading="busy[trackKey(track)] === 'dismiss'" :disabled="acting" @click="dismissOne(track)">不再推荐</n-button>
        </template>
      </div>
    </article>

    <div v-if="searched && !searching && !tracks.length && !error" class="empty-well">{{ notice || '这批没有可入库的新歌。可以换个平台或数量。' }}</div>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NCard, NSelect, NTag, useDialog, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { dismissDiscoveryTracks, enqueueDiscoveryTracks, listDiscoveryJobs, rediscoverTracks, restoreDiscoveryTracks, type DiscoveryEnqueueResult, type DiscoveryJob, type DiscoveryTrack } from '../api'

defineOptions({ name: 'Rediscover' })

type RowState = 'queued' | 'dismissed' | 'skipped' | 'running' | 'pending_upload' | 'completed' | 'owned' | 'failed' | 'needs_confirmation' | 'ignored'
type BusyAction = 'enqueue' | 'dismiss' | 'restore'
type TagType = 'default' | 'success' | 'error' | 'warning' | 'info'

const message = useMessage()
const dialog = useDialog()
const platform = ref('netease')
const limit = ref(6)
const counts = [3, 6, 9]
const platformOptions = [
  { label: '网易云', value: 'netease' },
  { label: 'QQ 音乐', value: 'qq' },
  { label: 'YouTube', value: 'youtube' },
]
const searching = ref(false)
const batching = ref(false)
const searched = ref(false)
const notice = ref('')
const error = ref('')
const tracks = ref<DiscoveryTrack[]>([])
const states = ref<Record<string, RowState>>({})
const notes = ref<Record<string, string>>({})
const busy = ref<Record<string, BusyAction>>({})
const acting = computed(() => searching.value || batching.value || Object.keys(busy.value).length > 0)
const actionable = computed(() => tracks.value.filter((track) => !states.value[trackKey(track)]))
const stateNames: Record<RowState, string> = {
  queued: '排队',
  dismissed: '不再推荐',
  skipped: '未重复提交',
  running: '下载中',
  pending_upload: '待上传',
  completed: '已入库',
  owned: '曲库已有',
  failed: '失败',
  needs_confirmation: '待确认',
  ignored: '已跳过',
}
const activeStates = new Set<RowState>(['queued', 'running', 'pending_upload'])
let pollTimer = 0
let searchSeq = 0
let jobsSeq = 0

function formatDuration(sec: number) {
  const minutes = Math.floor(sec / 60)
  return `${minutes}:${String(sec % 60).padStart(2, '0')}`
}
function openListen(rawURL: string) {
  try {
    const url = new URL(rawURL)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new Error()
    window.open(url.href, '_blank', 'noopener,noreferrer')
  } catch {
    message.error('试听链接无效')
  }
}
function trackKey(track: DiscoveryTrack) {
  return `${track.platform}:${track.id}`
}
function stateOf(track: DiscoveryTrack) {
  return states.value[trackKey(track)] || ''
}
function stateLabel(track: DiscoveryTrack) {
  return stateNames[stateOf(track) as RowState] || '已提交入库'
}
function tagType(track: DiscoveryTrack): TagType {
  const state = stateOf(track)
  if (state === 'failed') return 'error'
  if (state === 'needs_confirmation' || state === 'pending_upload') return 'warning'
  if (state === 'queued' || state === 'running') return 'info'
  if (state === 'dismissed' || state === 'skipped' || state === 'ignored') return 'default'
  return 'success'
}
function noteOf(track: DiscoveryTrack) {
  return notes.value[trackKey(track)] || ''
}
function stopPoll() {
  if (!pollTimer) return
  window.clearInterval(pollTimer)
  pollTimer = 0
}
function startPoll() {
  if (pollTimer) return
  pollTimer = window.setInterval(() => { void refreshJobs() }, 2000)
}
async function refreshJobs() {
  if (!tracks.value.length) {
    stopPoll()
    return
  }
  const seq = ++jobsSeq
  try {
    const response = await listDiscoveryJobs()
    if (seq !== jobsSeq || response.code !== 200 || !response.data) return
    const byKey = new Map<string, DiscoveryJob>()
    for (const job of response.data.jobs || []) {
      const key = `${job.platform}:${job.id}`
      if (!byKey.has(key)) byKey.set(key, job)
    }
    const next = { ...states.value }
    const nextNotes = { ...notes.value }
    let active = false
    for (const track of tracks.value) {
      const key = trackKey(track)
      if (next[key] === 'dismissed') continue
      const job = byKey.get(key)
      if (!job || !(job.state in stateNames)) continue
      const state = job.state as RowState
      next[key] = state
      nextNotes[key] = state === 'failed' ? (job.error || '') : ''
      if (activeStates.has(state)) active = true
    }
    states.value = next
    notes.value = nextNotes
    if (active) startPoll()
    else stopPoll()
  } catch {
    stopPoll()
  }
}
function mark(list: DiscoveryTrack[], state: RowState) {
  const next = { ...states.value }
  for (const track of list) next[trackKey(track)] = state
  states.value = next
}

async function search() {
  const seq = ++searchSeq
  searching.value = true
  error.value = ''
  notice.value = ''
  tracks.value = []
  states.value = {}
  notes.value = {}
  stopPoll()
  try {
    const response = await rediscoverTracks(platform.value, limit.value)
    if (seq !== searchSeq) return
    if (response.code !== 200 || !response.data) throw new Error(response.message || '找回失败')
    tracks.value = response.data.tracks || []
    notice.value = response.data.notice || ''
    searched.value = true
  } catch (err) {
    if (seq !== searchSeq) return
    tracks.value = []
    error.value = err instanceof Error ? err.message : '找回失败'
  } finally {
    if (seq === searchSeq) searching.value = false
  }
}

async function enqueue(list: DiscoveryTrack[]) {
  const pending = list.filter((track) => !states.value[trackKey(track)])
  if (!pending.length) return
  const response = await enqueueDiscoveryTracks(pending)
  if (response.code !== 200 || !response.data) {
    await refreshJobs()
    throw new Error(response.message || '入库失败')
  }
  const queued = response.data.queued || 0
  const skipped = response.data.skipped || 0
  const failed = response.data.failed || 0
  applyEnqueueResults(pending, response.data.results || [])
  if (queued > 0) message.success(queued === 1 ? '已提交 1 首入库' : `已提交 ${queued} 首入库`)
  if (skipped > 0 || failed > 0) {
    const parts = [`提交 ${pending.length} 首`, `实际排队 ${queued} 首`]
    if (skipped > 0) parts.push(`跳过 ${skipped} 首`)
    if (failed > 0) parts.push(`失败 ${failed} 首`)
    message.warning(parts.join('，'))
  }
  if (queued > 0) startPoll()
  await refreshJobs()
}

function applyEnqueueResults(pending: DiscoveryTrack[], results: DiscoveryEnqueueResult[]) {
  const byKey = new Map(results.map((result) => [`${result.platform}:${result.id}`, result]))
  const nextStates = { ...states.value }
  const nextNotes = { ...notes.value }
  for (const track of pending) {
    const key = trackKey(track)
    const result = byKey.get(key)
    if (!result) continue
    nextStates[key] = result.state === 'queued' ? 'queued' : result.state === 'failed' ? 'failed' : 'skipped'
    if (result.reason) nextNotes[key] = result.reason
  }
  states.value = nextStates
  notes.value = nextNotes
}

async function enqueueOne(track: DiscoveryTrack) {
  const key = trackKey(track)
  if (states.value[key] || busy.value[key]) return
  busy.value = { ...busy.value, [key]: 'enqueue' }
  try {
    await enqueue([track])
  } catch (err) {
    message.error(err instanceof Error ? err.message : '入库失败')
  } finally {
    const next = { ...busy.value }
    delete next[key]
    busy.value = next
  }
}

async function dismissOne(track: DiscoveryTrack) {
  const key = trackKey(track)
  if (states.value[key] || busy.value[key]) return
  busy.value = { ...busy.value, [key]: 'dismiss' }
  try {
    const response = await dismissDiscoveryTracks([track])
    if (response.code !== 200) throw new Error(response.message || '没有记下')
    mark([track], 'dismissed')
    message.success('不再推荐')
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有记下')
  } finally {
    const next = { ...busy.value }
    delete next[key]
    busy.value = next
  }
}

async function restoreOne(track: DiscoveryTrack) {
  const key = trackKey(track)
  if (busy.value[key]) return
  busy.value = { ...busy.value, [key]: 'restore' }
  try {
    const response = await restoreDiscoveryTracks([track])
    if (response.code !== 200) throw new Error(response.message || '没有恢复')
    const nextStates = { ...states.value }
    delete nextStates[key]
    states.value = nextStates
    message.success('已恢复推荐')
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有恢复')
  } finally {
    const next = { ...busy.value }
    delete next[key]
    busy.value = next
  }
}

function confirmEnqueue() {
  const count = actionable.value.length
  if (!count || acting.value) return
  dialog.warning({
    title: '这组入库',
    content: `将把这 ${count} 首提交到下载队列。这一步只是排队，不会当场变成已入库。`,
    positiveText: '确认入库',
    negativeText: '返回',
    maskClosable: false,
    onPositiveClick: () => submitBatch(),
  })
}

async function submitBatch() {
  batching.value = true
  try {
    await enqueue(actionable.value)
  } catch (err) {
    message.error(err instanceof Error ? err.message : '入库失败')
  } finally {
    batching.value = false
  }
}

onActivated(() => {
  if (tracks.value.some((track) => activeStates.has(stateOf(track) as RowState))) startPoll()
})
onDeactivated(stopPoll)
onUnmounted(stopPoll)
</script>

<style scoped>
.studio-page { max-width: 980px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.studio-card, .track-card { background: rgba(16, 22, 26, .42); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px); }
.studio-help { margin: 12px 0 0; color: #7d8c86; font-size: 12px; line-height: 1.6; }
.control-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.platform-select { width: 160px; }
.empty-well { padding: 22px 16px; border: 1px dashed rgba(125,150,141,.24); border-radius: 16px; color: #8ea39b; line-height: 1.6; }
.skel { display: block; height: 12px; margin-bottom: 8px; border-radius: 999px; background: rgba(255,255,255,.06); }
.skel.short { width: 55%; margin-bottom: 0; }
.count-row { display: flex; gap: 6px; }
.count-chip {
  min-height: 30px; padding: 4px 12px; border: 1px solid rgba(171, 190, 184, .16); border-radius: 999px;
  color: #8c9a94; background: rgba(16, 22, 26, .4); cursor: pointer;
  transition: color .22s var(--ease, ease), border-color .22s var(--ease, ease), background .22s var(--ease, ease);
}
.count-chip.active { color: #06140f; border-color: #63e2b7; background: #63e2b7; }
.result-head, .track-card { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.result-head { color: #8ea39b; font-size: 13px; }
.track-card { padding: 14px 16px; border: 1px solid rgba(125, 150, 141, .16); border-radius: 16px; }
.track-title { color: #d7fff0; font-weight: 600; text-decoration: none; }
a.track-title:hover { color: #63e2b7; }
.track-copy p { margin: 4px 0 0; color: #8ea39b; font-size: 13px; }
.track-meta, .track-note { font-size: 12px; }
.track-note { color: #d9a06a; }
.track-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
@media (max-width: 700px) {
  .track-card, .result-head { align-items: flex-start; flex-direction: column; }
  .platform-select { width: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  .count-chip { transition: none; }
}
</style>
