<template>
  <section class="studio-page">
    <PageHeading eyebrow="AI / PLAYLIST" title="生成歌单">
      <template #actions>
        <n-button size="small" quaternary :loading="loading" @click="load">刷新歌单</n-button>
      </template>
    </PageHeading>

    <n-alert v-if="warning" type="warning" :show-icon="false">{{ warning }}</n-alert>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>

    <n-card size="small" :bordered="false" class="studio-card">
      <div class="mode-row" role="group" aria-label="歌单目标">
        <button type="button" class="mode-chip" :class="{ active: mode === 'existing' }" @click="mode = 'existing'">已有歌单</button>
        <button type="button" class="mode-chip" :class="{ active: mode === 'new' }" @click="mode = 'new'">新建歌单</button>
      </div>
      <n-select
        v-if="mode === 'existing'"
        v-model:value="playlistId"
        :options="playlistOptions"
        placeholder="选择一张 [AI] 歌单"
        class="field"
      />
      <n-input v-else v-model:value="newName" placeholder="新歌单名称，会自动加上 [AI]" class="field" />
      <div class="preset-row">
        <button
          v-for="preset in presets"
          :key="preset.id"
          type="button"
          class="mode-chip"
          :class="{ active: selectedPreset === preset.id }"
          @click="togglePreset(preset.id)"
        >{{ preset.label }}</button>
      </div>
      <n-input v-model:value="seedText" placeholder="按某首歌找相似，例如：晴天 - 周杰伦" class="field" />
      <n-input
        v-model:value="requestText"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 5 }"
        placeholder="补充想听的主题。只选上面的氛围，或只填一首种子歌也可以。"
      />
      <div class="form-actions">
        <n-button type="primary" :loading="previewing" :disabled="confirming" @click="preview">生成预览</n-button>
      </div>
      <p class="studio-help">预览只展示前 10 首。确认后按服务端保存的整张歌单写入，页面不会再把曲目列表传回去。</p>
    </n-card>

    <n-card v-if="previewPage" size="small" :bordered="false" class="studio-card">
      <header class="preview-head">
        <div>
          <h3>{{ previewPage.name }}</h3>
          <p>{{ previewPage.description || '还没有描述' }}</p>
        </div>
        <n-tag :bordered="false" type="info">共 {{ previewPage.total }} 首</n-tag>
      </header>
      <p v-if="previewPage.intro" class="intro">{{ previewPage.intro }}</p>
      <ol class="preview-list">
        <li v-for="(song, index) in previewPage.preview" :key="`${song.title}-${index}`">
          <strong>{{ song.title || '未命名' }}</strong>
          <span>{{ song.artist || '未知歌手' }}</span>
          <em v-if="song.reason">{{ song.reason }}</em>
        </li>
      </ol>
      <p v-if="previewPage.total > previewPage.preview.length" class="studio-help">这里是前 {{ previewPage.preview.length }} 首。确认会写入全部 {{ previewPage.total }} 首。</p>
      <div class="form-actions">
        <n-button type="primary" :loading="confirming" :disabled="previewing" @click="confirm">确认写入</n-button>
      </div>
    </n-card>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NInput, NSelect, NTag, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { confirmAIPlaylist, listAIPlaylists, previewAIPlaylist, type AIPlaylistItem, type AIPreset, type AIPreviewPage } from '../api'

defineOptions({ name: 'AIPlaylist' })

const message = useMessage()
const loading = ref(false)
const previewing = ref(false)
const confirming = ref(false)
const error = ref('')
const warning = ref('')
const presets = ref<AIPreset[]>([])
const playlists = ref<AIPlaylistItem[]>([])
const mode = ref<'existing' | 'new'>('existing')
const playlistId = ref<string | null>(null)
const newName = ref('')
const selectedPreset = ref('')
const requestText = ref('')
const seedText = ref('')
const previewPage = ref<AIPreviewPage | null>(null)
let skipActivate = true

const playlistOptions = computed(() => playlists.value.map((item) => ({
  label: `${item.name} · ${item.song_count} 首`,
  value: item.id,
})))

function togglePreset(id: string) {
  selectedPreset.value = selectedPreset.value === id ? '' : id
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await listAIPlaylists()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '读取歌单失败')
    presets.value = response.data.presets || []
    playlists.value = response.data.playlists || []
    warning.value = response.data.warning || ''
    if (playlistId.value && !playlists.value.some((item) => item.id === playlistId.value)) playlistId.value = null
    if (!playlists.value.length) mode.value = 'new'
  } catch (err) {
    error.value = err instanceof Error ? err.message : '读取歌单失败'
  } finally {
    loading.value = false
  }
}

async function preview() {
  const body = mode.value === 'existing'
    ? { playlist_id: playlistId.value || '', name: '', request: requestText.value, preset: selectedPreset.value, seed: seedText.value }
    : { playlist_id: '', name: newName.value, request: requestText.value, preset: selectedPreset.value, seed: seedText.value }
  if (mode.value === 'existing' && !body.playlist_id) {
    message.warning('请选择已有歌单，或改成新建')
    return
  }
  if (mode.value === 'new' && !newName.value.trim()) {
    message.warning('请填写新歌单名称')
    return
  }
  if (!selectedPreset.value && !requestText.value.trim() && !seedText.value.trim()) {
    message.warning('请选择氛围，写明主题，或填一首种子歌')
    return
  }
  previewing.value = true
  error.value = ''
  try {
    const response = await previewAIPlaylist(body)
    if (response.code !== 200 || !response.data?.token) throw new Error(response.message || '预览失败')
    previewPage.value = { ...response.data, preview: response.data.preview || [] }
  } catch (err) {
    message.error(err instanceof Error ? err.message : '预览失败')
  } finally {
    previewing.value = false
  }
}

async function confirm() {
  const token = previewPage.value?.token
  if (!token) return
  confirming.value = true
  try {
    const response = await confirmAIPlaylist(token)
    if (response.code !== 200 || !response.data) throw new Error(response.message || '写入失败')
    const page = response.data
    message.success(page.created ? `已创建「${page.name}」，共 ${page.count} 首` : `已更新「${page.name}」，共 ${page.count} 首`)
    previewPage.value = null
    await load()
  } catch (err) {
    const text = err instanceof Error ? err.message : '写入失败'
    if (text.includes('预览已过期')) previewPage.value = null
    message.error(text)
  } finally {
    confirming.value = false
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
</script>

<style scoped>
.studio-page { max-width: 860px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.studio-card { background: rgba(16, 22, 26, .42); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px); }
.mode-row, .preset-row, .form-actions, .preview-head { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.preset-row, .field, .form-actions { margin-top: 12px; }
.mode-chip {
  min-height: 30px; padding: 4px 12px; border: 1px solid rgba(171, 190, 184, .16); border-radius: 999px;
  color: #8c9a94; background: rgba(16, 22, 26, .4); cursor: pointer;
}
.mode-chip.active { color: #06140f; border-color: #63e2b7; background: #63e2b7; }
.studio-help, .intro, .preview-head p { color: #7d8c86; font-size: 12px; line-height: 1.6; }
.studio-help { margin: 12px 0 0; }
.preview-head { justify-content: space-between; }
.preview-head h3 { margin: 0; font-size: 18px; }
.preview-head p, .intro { margin: 4px 0 0; }
.preview-list { margin: 12px 0 0; padding-left: 18px; }
.preview-list li { margin-top: 8px; }
.preview-list span, .preview-list em { display: block; color: #8ea39b; font-size: 12px; font-style: normal; }
</style>
