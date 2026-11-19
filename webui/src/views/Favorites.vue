<template>
  <section class="studio-page">
    <PageHeading eyebrow="DISCOVERY / FAVORITES" title="收藏同步">
      <template #actions>
        <n-button size="small" quaternary :loading="loading" @click="load()">刷新</n-button>
      </template>
    </PageHeading>

    <n-card size="small" :bordered="false" class="studio-card">
      <p class="studio-help intro-copy">{{ helpText }}</p>
      <div class="queue-row">
        <div class="queue-chips">
          <span v-if="!page?.queue.length" class="queue-chip quiet">队列空闲</span>
          <span v-for="item in page?.queue || []" :key="item.state" class="queue-chip">{{ item.count }} {{ item.label }}</span>
        </div>
        <div class="track-actions">
          <n-button v-if="page?.enabled" size="small" type="primary" secondary :loading="syncing" :disabled="acting" @click="syncNow">现在同步</n-button>
          <n-button size="small" quaternary :loading="refreshing" :disabled="acting" @click="refreshQueue">刷新状态</n-button>
          <n-button v-if="failedCount" size="small" secondary :loading="retrying" :disabled="acting" @click="retryFailed">失败的重新排队</n-button>
        </div>
      </div>
    </n-card>

    <n-alert v-if="notice" :type="noticeType" :show-icon="false">{{ notice }}</n-alert>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>

    <article v-for="item in platforms" :key="item.platform" class="track-card">
      <div class="track-copy">
        <span class="track-title">{{ item.label }}</span>
        <p>{{ item.state }}<template v-if="item.account"> · {{ item.account }}</template></p>
        <p class="track-meta">{{ item.last_sync ? `上次同步 ${item.last_sync}` : '还没有同步过' }}</p>
        <p v-if="item.error" class="track-note">{{ item.error }}</p>
      </div>
      <div class="track-actions">
        <n-button
          size="small"
          :type="item.bound ? 'default' : 'primary'"
          :secondary="item.bound"
          :loading="previewing === item.platform"
          :disabled="acting && previewing !== item.platform"
          @click="readFavorites(item.platform)"
        >{{ item.bound ? '重新读取' : '读取收藏' }}</n-button>
        <n-button
          v-if="item.bound"
          size="small"
          quaternary
          :loading="pausing === item.platform"
          :disabled="acting"
          @click="togglePause(item)"
        >{{ item.paused ? '恢复' : '暂停' }}</n-button>
      </div>
    </article>

    <article v-if="preview" class="preview-panel">
      <div>
        <h3>确认 {{ platformLabel(preview.platform) }} 账号</h3>
        <p>账号 {{ preview.account || '未命名' }} · 读到 {{ preview.count }} 首收藏</p>
        <p class="studio-help">确认后只建立基线。这批已经收藏的歌不会下载。之后新增的收藏{{ preview.enabled ? '会进入上面的入库队列' : '也不会自动入库，因为自动同步没开' }}。</p>
      </div>
      <ul v-if="preview.samples?.length" class="sample-list">
        <li v-for="(song, index) in preview.samples" :key="`${song.title}-${index}`">
          <strong>{{ song.title }}</strong>
          <span>{{ song.artist || '未知歌手' }}</span>
        </li>
      </ul>
      <p v-else class="studio-help">这批收藏没有可展示的歌名。</p>
      <div class="track-actions">
        <n-button size="small" type="primary" :loading="binding" :disabled="acting" @click="bind(preview.token)">确认并建立基线</n-button>
        <n-button size="small" quaternary :disabled="binding" @click="preview = null">取消</n-button>
      </div>
    </article>

    <div v-if="page?.pending.length" class="result-head">
      <span>待确认</span>
    </div>
    <article v-for="job in page?.pending || []" :key="job.id" class="track-card">
      <div class="track-copy">
        <span class="track-title">{{ job.title || '未命名' }}</span>
        <p>{{ job.artist || '未知歌手' }} · {{ platformLabel(job.platform) }}</p>
        <p class="track-note">确认后会入库。不再处理之后，这首不会再出现。</p>
      </div>
      <div class="track-actions">
        <n-button size="small" type="primary" secondary :loading="resolving === job.id + ':1'" :disabled="acting" @click="resolve(job.id, true)">确认</n-button>
        <n-button size="small" quaternary :loading="resolving === job.id + ':0'" :disabled="acting" @click="resolve(job.id, false)">不再处理</n-button>
      </div>
    </article>

    <div v-if="page?.failed.length" class="result-head">
      <span>失败 {{ failedCount }} 首</span>
    </div>
    <article v-for="job in page?.failed || []" :key="job.id" class="track-card">
      <div class="track-copy">
        <span class="track-title">{{ job.title || '未命名' }}</span>
        <p>{{ job.artist || '未知歌手' }} · {{ platformLabel(job.platform) }}</p>
        <p class="track-note">{{ job.error || '没有留下失败原因。' }}</p>
      </div>
    </article>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NCard, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import {
  bindFavorites, getFavorites, pauseFavorites, previewFavorites, refreshFavorites, resolveFavorite, retryFavorites, syncFavorites,
  type FavoritePlatform, type FavoritePreview, type FavoritesPage,
} from '../api'

defineOptions({ name: 'Favorites' })

const message = useMessage()
const loading = ref(false)
const syncing = ref(false)
const refreshing = ref(false)
const retrying = ref(false)
const previewing = ref('')
const pausing = ref('')
const resolving = ref('')
const binding = ref(false)
const preview = ref<FavoritePreview | null>(null)
const error = ref('')
const notice = ref('')
const page = ref<FavoritesPage | null>(null)
let pollTimer = 0
let skipActivate = true

const platforms = computed(() => page.value?.platforms || [])
const failedCount = computed(() => page.value?.queue.find((item) => item.state === 'failed')?.count || page.value?.failed.length || 0)
const acting = computed(() => loading.value || syncing.value || refreshing.value || retrying.value || binding.value || previewing.value !== '' || pausing.value !== '' || resolving.value !== '')
const helpText = computed(() => {
  if (!page.value) return '读取收藏只是预览。确认后才建立基线，这批已有收藏不会下载。'
  if (page.value.enabled) return `每 ${page.value.poll_minutes} 分钟检查一次。新收藏会出现在这页的入库队列里。确认绑定只建立基线，这批已有收藏不会下载。`
  return '自动同步没开。绑定之后，新增收藏也不会自动入库。确认绑定只建立基线，这批已有收藏不会下载。'
})
const noticeType = computed(() => (notice.value.includes('失败') || notice.value.includes('没有完成') ? 'warning' : 'success') as 'warning' | 'success')
const platformNames: Record<string, string> = { netease: '网易云', qq: 'QQ 音乐', youtube: 'YouTube' }

function platformLabel(platform: string) {
  return platformNames[platform] || platform
}
function applyPage(data: FavoritesPage, keepNotice = false) {
  page.value = {
    ...data,
    platforms: data.platforms || [],
    queue: data.queue || [],
    failed: data.failed || [],
    pending: data.pending || [],
  }
  if (!keepNotice) notice.value = data.notice || ''
  const active = (data.queue || []).some((item) => item.state === 'queued' || item.state === 'running' || item.state === 'pending_upload')
  if (active) startPoll()
  else stopPoll()
}
function stopPoll() {
  if (!pollTimer) return
  window.clearInterval(pollTimer)
  pollTimer = 0
}
function startPoll() {
  if (pollTimer) return
  pollTimer = window.setInterval(() => { void load(true) }, 3000)
}
async function load(quiet = false) {
  if (!quiet) loading.value = true
  try {
    const response = await getFavorites()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '读取收藏状态失败')
    applyPage(response.data, quiet)
    if (!quiet) error.value = ''
  } catch (err) {
    if (!quiet) error.value = err instanceof Error ? err.message : '读取收藏状态失败'
    if (!quiet) stopPoll()
  } finally {
    if (!quiet) loading.value = false
  }
}
async function runAction(kind: 'sync' | 'refresh' | 'retry') {
  const flag = kind === 'sync' ? syncing : kind === 'refresh' ? refreshing : retrying
  flag.value = true
  error.value = ''
  try {
    const response = kind === 'sync' ? await syncFavorites() : kind === 'refresh' ? await refreshFavorites() : await retryFavorites()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '操作失败')
    applyPage(response.data)
  } catch (err) {
    message.error(err instanceof Error ? err.message : '操作失败')
  } finally {
    flag.value = false
  }
}
function syncNow() { void runAction('sync') }
function refreshQueue() { void runAction('refresh') }
function retryFailed() { void runAction('retry') }
async function togglePause(item: FavoritePlatform) {
  pausing.value = item.platform
  error.value = ''
  try {
    const response = await pauseFavorites(item.platform, !item.paused)
    if (response.code !== 200 || !response.data) throw new Error(response.message || '没有改成')
    applyPage(response.data)
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有改成')
  } finally {
    pausing.value = ''
  }
}
async function readFavorites(platform: string) {
  previewing.value = platform
  error.value = ''
  try {
    const response = await previewFavorites(platform)
    if (response.code !== 200 || !response.data?.token) throw new Error(response.message || '读取收藏失败')
    preview.value = { ...response.data, samples: response.data.samples || [] }
  } catch (err) {
    message.error(err instanceof Error ? err.message : '读取收藏失败')
  } finally {
    previewing.value = ''
  }
}
async function bind(token: string) {
  binding.value = true
  try {
    const response = await bindFavorites(token)
    if (response.code !== 200 || !response.data) throw new Error(response.message || '没有建立基线')
    preview.value = null
    applyPage(response.data)
    message.success('已建立收藏基线')
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有建立基线')
  } finally {
    binding.value = false
  }
}
async function resolve(id: string, accept: boolean) {
  resolving.value = `${id}:${accept ? 1 : 0}`
  error.value = ''
  try {
    const response = await resolveFavorite(id, accept)
    if (response.code !== 200 || !response.data) throw new Error(response.message || '没有处理成')
    applyPage(response.data)
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有处理成')
  } finally {
    resolving.value = ''
  }
}

onMounted(() => {
  skipActivate = true
  void load()
})
onActivated(() => {
  if (skipActivate) {
    skipActivate = false
    return
  }
  void load()
})
onDeactivated(stopPoll)
onUnmounted(stopPoll)
</script>

<style scoped>
.studio-page { max-width: 980px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.studio-card, .track-card { background: rgba(16, 22, 26, .42); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px); }
.intro-copy { margin: 0; }
.studio-help, .track-note { color: #7d8c86; font-size: 12px; line-height: 1.6; }
.track-note { margin: 4px 0 0; color: #d9a06a; }
.queue-row, .result-head, .track-card { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.queue-row { margin-top: 14px; color: #d7e4de; font-size: 13px; }
.queue-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.queue-chip { padding: 3px 8px; border-radius: 999px; background: rgba(99,226,183,.1); color: #d7fff0; font-size: 12px; }
.queue-chip.quiet { background: rgba(255,255,255,.04); color: #8ea39b; }
.preview-panel { display: flex; flex-direction: column; align-items: stretch; gap: 12px; padding: 16px; border: 1px solid rgba(99,226,183,.28); border-radius: 16px; background: rgba(16, 22, 26, .55); }
.preview-panel h3 { margin: 0; color: #edf7f3; font-size: 16px; }
.preview-panel p { margin: 6px 0 0; }
.sample-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 0; padding: 0; list-style: none; }
.sample-list li { padding: 8px 10px; border-radius: 12px; background: rgba(255,255,255,.03); }
.sample-list strong, .sample-list span { display: block; }
.sample-list strong { color: #edf7f3; }
.sample-list span { color: #8ea39b; font-size: 12px; }
.track-meta { color: #8ea39b; }
.result-head { color: #8ea39b; font-size: 13px; }
.track-card { padding: 14px 16px; border: 1px solid rgba(125, 150, 141, .16); border-radius: 16px; }
.track-title { color: #d7fff0; font-weight: 600; }
.track-copy p { margin: 4px 0 0; color: #8ea39b; font-size: 13px; }
.track-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; flex-wrap: wrap; }
@media (max-width: 700px) {
  .track-card, .queue-row, .result-head { align-items: flex-start; flex-direction: column; }
  .sample-list { grid-template-columns: 1fr; }
}
</style>
