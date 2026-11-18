<template>
  <section class="studio-page">
    <PageHeading eyebrow="LIBRARY / DUPLICATE REVIEW" title="重复歌曲">
      <template #actions>
        <n-button type="primary" :loading="starting || running" :disabled="running" @click="scan">
          {{ running ? '查重进行中' : '开始查重' }}
        </n-button>
      </template>
    </PageHeading>

    <n-alert type="info" :show-icon="false">查重只分组。选定留下的版本后，才会删掉同组里勾选的文件，并清掉曲库记录和向量。</n-alert>
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
          <label class="choice">
            <input type="radio" :name="groupKey(group, index)" :value="track.path" :checked="keepOf[groupKey(group, index)] === track.path" @change="chooseKeep(group, index, track.path)" />
            留下
          </label>
          <div>
            <strong>{{ track.name || track.path }}</strong>
            <p>{{ track.path }}</p>
          </div>
          <label v-if="keepOf[groupKey(group, index)] !== track.path" class="choice">
            <input type="checkbox" :checked="isRemoved(group, index, track.path)" @change="toggleRemove(group, index, track.path)" />
            删除
          </label>
          <a :href="streamUrl(track.path)" target="_blank" rel="noopener noreferrer">试听</a>
        </li>
      </ul>
      <p v-if="group.kind !== 'same'" class="studio-help">这组只是听感接近。要删哪一版，需要你自己勾。</p>
      <n-button size="small" type="primary" :disabled="running || applying || !canApply(group, index)" :loading="applying" @click="apply(group, index)">
        留下所选
      </n-button>
    </article>

    <n-empty v-if="!running && report.status === 'done' && !report.groups.length" description="这次没有发现重复或相似的歌曲" />
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { h } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NTag, useDialog, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { getFileStreamUrl, getLibraryDuplicates, keepLibraryDuplicate, startLibraryDuplicateScan, type LibraryDuplicateGroup, type LibraryDuplicateReport } from '../api'

defineOptions({ name: 'Duplicates' })

const message = useMessage()
const dialog = useDialog()
const starting = ref(false)
const applying = ref(false)
const error = ref('')
const keepOf = ref<Record<string, string>>({})
const removeOf = ref<Record<string, string[]>>({})
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

function groupKey(group: LibraryDuplicateGroup, index: number) {
  return `${group.kind}-${index}-${(group.tracks || []).map((track) => track.path).join('|')}`
}
function chooseKeep(group: LibraryDuplicateGroup, index: number, path: string) {
  const key = groupKey(group, index)
  keepOf.value = { ...keepOf.value, [key]: path }
  removeOf.value = { ...removeOf.value, [key]: (removeOf.value[key] || []).filter((item) => item !== path) }
}
function isRemoved(group: LibraryDuplicateGroup, index: number, path: string) {
  return (removeOf.value[groupKey(group, index)] || []).includes(path)
}
function toggleRemove(group: LibraryDuplicateGroup, index: number, path: string) {
  const key = groupKey(group, index)
  const current = removeOf.value[key] || []
  const next = current.includes(path) ? current.filter((item) => item !== path) : [...current, path]
  removeOf.value = { ...removeOf.value, [key]: next }
}
function canApply(group: LibraryDuplicateGroup, index: number) {
  const key = groupKey(group, index)
  const keep = keepOf.value[key]
  const remove = (removeOf.value[key] || []).filter((path) => path && path !== keep)
  return Boolean(keep && remove.length)
}
function seedChoices(next: LibraryDuplicateReport) {
  const keep: Record<string, string> = {}
  const remove: Record<string, string[]> = {}
  for (const [index, group] of (next.groups || []).entries()) {
    const key = groupKey(group, index)
    const paths = (group.tracks || []).map((track) => track.path).filter(Boolean)
    if (!paths.length) continue
    keep[key] = keepOf.value[key] && paths.includes(keepOf.value[key]) ? keepOf.value[key] : paths[0]
    const previous = (removeOf.value[key] || []).filter((path) => path !== keep[key] && paths.includes(path))
    remove[key] = previous.length || group.kind !== 'same' ? previous : paths.filter((path) => path !== keep[key])
  }
  keepOf.value = keep
  removeOf.value = remove
}
function apply(group: LibraryDuplicateGroup, index: number) {
  const key = groupKey(group, index)
  const keep = keepOf.value[key]
  const remove = (removeOf.value[key] || []).filter((path) => path && path !== keep)
  if (!keep || !remove.length || applying.value) return
  const names = remove.map((path) => path.split('/').pop() || path).join('\n')
  dialog.warning({
    title: '留下这一版',
    content: () => h('div', { style: 'white-space: pre-line; line-height: 1.8' }, `留下\n${keep.split('/').pop() || keep}\n\n删除\n${names}\n\n曲库记录和向量会一起清掉。`),
    positiveText: '删除勾选的版本',
    negativeText: '取消',
    maskClosable: false,
    onPositiveClick: () => { void commitKeep(keep, remove) },
  })
}
async function commitKeep(keep: string, remove: string[]) {
  applying.value = true
  error.value = ''
  try {
    const response = await keepLibraryDuplicate(keep, remove)
    if (response.code !== 200) throw new Error(response.message || '没有删成')
    message.success('已留下所选版本')
    await refresh()
  } catch (err) {
    error.value = err instanceof Error ? err.message : '没有删成'
    message.error(error.value)
    await refresh()
  } finally {
    applying.value = false
  }
}
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
    seedChoices(report.value)
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
      seedChoices(report.value)
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
.group-card .n-button { margin-top: 12px; }
.choice { display: flex; align-items: center; gap: 6px; color: #d5e4de; font-size: 13px; flex-shrink: 0; }
@media (max-width: 700px) {
  .group-card li { align-items: flex-start; }
}
</style>
