<template>
  <section class="studio-page">
    <PageHeading eyebrow="LIBRARY / PLAYLIST NOTES" title="歌单描述">
      <template #actions>
        <n-button size="small" :loading="syncing" :disabled="loading" @click="sync">同步歌单</n-button>
        <n-button size="small" quaternary :loading="loading" @click="load">刷新</n-button>
      </template>
    </PageHeading>

    <n-alert v-if="!loading && !synced" type="info" :show-icon="false">还没和 Navidrome 同步过。同步后这里才会出现可编辑的歌单。</n-alert>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
    <p class="studio-help">只编辑整理用的说明，最多 200 字。歌单、回收站和 [AI] 歌单不在这里。</p>

    <n-spin :show="loading">
      <article v-for="item in items" :key="item.id" class="desc-card">
        <header>
          <h3>{{ item.name }}</h3>
          <span>{{ runeCount(drafts[item.id] ?? '') }} / 200</span>
        </header>
        <n-input
          :value="drafts[item.id] ?? ''"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 5 }"
          placeholder="给这张歌单写一句整理说明"
          @update:value="updateDraft(item.id, $event)"
        />
        <div class="desc-actions">
          <n-button size="small" type="primary" secondary :loading="saving === item.id" :disabled="!dirty(item)" @click="save(item)">保存描述</n-button>
        </div>
      </article>
      <n-empty v-if="!loading && !items.length && !error" description="当前没有可编辑的歌单" />
    </n-spin>
  </section>
</template>

<script setup lang="ts">
import { onActivated, onMounted, ref } from 'vue'
import { NAlert, NButton, NEmpty, NInput, NSpin, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { listPlaylistDescriptions, syncPlaylistCatalog, updatePlaylistDescription, type PlaylistDescription } from '../api'

defineOptions({ name: 'PlaylistDesc' })

const message = useMessage()
const items = ref<PlaylistDescription[]>([])
const drafts = ref<Record<string, string>>({})
const synced = ref(true)
const loading = ref(false)
const syncing = ref(false)
const saving = ref('')
const error = ref('')
let skipActivate = true

function runeCount(value: string) {
  return Array.from(value.trim()).length
}
function dirty(item: PlaylistDescription) {
  return (drafts.value[item.id] ?? '') !== item.desc
}
function updateDraft(id: string, value: string) {
  drafts.value = { ...drafts.value, [id]: value }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await listPlaylistDescriptions()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '读取歌单失败')
    const next = response.data.items || []
    const kept: Record<string, string> = {}
    for (const item of next) {
      const current = drafts.value[item.id]
      kept[item.id] = current !== undefined && current !== item.desc && items.value.some((row) => row.id === item.id && row.desc === item.desc)
        ? current
        : item.desc
    }
    items.value = next
    drafts.value = kept
    synced.value = !!response.data.synced
  } catch (err) {
    error.value = err instanceof Error ? err.message : '读取歌单失败'
  } finally {
    loading.value = false
  }
}

async function save(item: PlaylistDescription) {
  const desc = drafts.value[item.id] ?? ''
  if (runeCount(desc) > 200) {
    message.error('描述最多 200 字')
    return
  }
  saving.value = item.id
  try {
    const response = await updatePlaylistDescription(item.id, desc)
    if (response.code !== 200 || !response.data) throw new Error(response.message || '保存失败')
    const saved = response.data.desc
    items.value = items.value.map((row) => row.id === item.id ? { ...row, desc: saved } : row)
    drafts.value = { ...drafts.value, [item.id]: saved }
    message.success('描述已保存')
  } catch (err) {
    message.error(err instanceof Error ? err.message : '保存失败')
  } finally {
    saving.value = ''
  }
}

async function sync() {
  syncing.value = true
  try {
    const response = await syncPlaylistCatalog()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '同步失败')
    const result = response.data
    message.success(`已同步：新增 ${result.added}，更新 ${result.updated}，停用 ${result.inactive}`)
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : '同步失败')
  } finally {
    syncing.value = false
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
  const unsaved = items.value.some(dirty)
  if (!unsaved) void load()
})
</script>

<style scoped>
.studio-page { max-width: 860px; margin: 0 auto; display: flex; flex-direction: column; gap: 12px; }
.studio-help { margin: 0; color: #7d8c86; font-size: 12px; }
.desc-card {
  margin-bottom: 12px; padding: 16px; border: 1px solid rgba(125, 150, 141, .16); border-radius: 16px;
  background: rgba(16, 22, 26, .42); backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px);
}
.desc-card header, .desc-actions { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.desc-card h3 { margin: 0 0 10px; font-size: 16px; }
.desc-card header span { color: #7d8c86; font-size: 12px; }
.desc-actions { margin-top: 10px; justify-content: flex-end; }
</style>
