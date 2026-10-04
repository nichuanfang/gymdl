<template>
  <section class="settings-page">
    <header class="page-heading">
      <div>
        <p class="eyebrow">APPLICATION / CONFIGURATION</p>
        <n-h2>设置</n-h2>
      </div>
      <n-space :size="8">
        <n-popconfirm
          v-if="restartSupported"
          positive-text="重启"
          negative-text="取消"
          @positive-click="handleRestart"
        >
          <template #trigger>
            <n-button type="warning" secondary :loading="restarting" :disabled="loading || saving">重启应用</n-button>
          </template>
          重启会中断当前程序操作。仅在 Docker 配置了 restart 策略时会自动重新启动；请确认没有其他正在执行的任务。
        </n-popconfirm>
        <n-button type="primary" :loading="saving" :disabled="loading || !config" @click="handleSaveConfig">保存全部设置</n-button>
      </n-space>
    </header>

    <n-alert v-if="restartFields.length" type="warning" :show-icon="false" class="restart-alert">
      设置已保存。以下配置需重启服务后生效：{{ restartFields.join('、') }}
    </n-alert>
    <n-alert v-if="hotReloadFields.length" type="success" :show-icon="false" class="restart-alert">
      以下字段已热加载：{{ hotReloadFields.join('、') }}。更改仅影响新任务和后续文件库请求。
    </n-alert>

    <n-spin :show="loading">
      <div v-if="config" class="settings-layout">
        <n-tabs v-model:value="activeSection" type="line" placement="left" class="settings-tabs">
          <n-tab-pane v-for="key in sectionKeys" :key="key" :name="key" :tab="sectionLabel(key)">
            <n-card size="small" :title="sectionLabel(key)" class="section-card">
              <p class="section-help">修改后统一校验并安全写回配置文件；密钥字段不会回显，留空保持原值。</p>
              <ConfigEditorFields
                :model-value="config[key]"
                :path="key"
                :clear-secrets="clearSecrets"
                @update:model-value="config[key] = $event"
                @update:clear-secrets="clearSecrets = $event"
              />
            </n-card>
          </n-tab-pane>
        </n-tabs>
      </div>
    </n-spin>

    <n-card title="CookieCloud 状态" size="small" class="cookie-card">
      <n-space align="center" :size="12" wrap>
        <n-tag :type="cookieStatusLoading || cookieStatusFailed ? 'default' : (cc?.available ? 'success' : 'warning')" size="small">
          {{ cookieStatusLoading ? '查询中…' : (cookieStatusFailed ? '状态未知' : (cc?.available ? '在线' : '离线 / 未初始化')) }}
        </n-tag>
        <n-tag :type="cookieStatusLoading || cookieStatusFailed ? 'default' : (cc?.file_exists ? 'info' : 'default')" size="small">
          {{ cookieStatusLoading ? '检查 Cookie 文件…' : (cookieStatusFailed ? '文件状态未知' : (cc?.file_exists ? 'Cookie 文件存在' : '无 Cookie')) }}
        </n-tag>
        <n-text v-if="cc?.file_mod_time" depth="3" style="font-size: 11px">更新于 {{ new Date(cc.file_mod_time).toLocaleString('zh-CN', { hour12: false }) }}</n-text>
        <n-button size="small" :loading="syncing" @click="handleSync">立即同步</n-button>
      </n-space>
    </n-card>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  NH2, NButton, NCard, NSpace, NSpin, NTabs, NTabPane, NAlert, NTag, NText, NPopconfirm, useMessage,
} from 'naive-ui'
import ConfigEditorFields from '../components/ConfigEditorFields.vue'
import { getSystemConfig, getSystemStatus, getCookieCloudStatus, syncCookieCloud, updateSystemConfig, requestApplicationRestart } from '../api'
import type { CookieCloudStatus } from '../types'

defineOptions({ name: 'Settings' })

const message = useMessage()
const loading = ref(true)
const saving = ref(false)
const syncing = ref(false)
const restarting = ref(false)
const restartSupported = ref(false)
const cookieStatusLoading = ref(true)
const cookieStatusFailed = ref(false)
const config = ref<Record<string, any> | null>(null)
const cc = ref<CookieCloudStatus | null>(null)
const clearSecrets = ref<string[]>([])
const activeSection = ref('web_config')
const restartFields = ref<string[]>([])
const hotReloadFields = ref<string[]>([])
const sectionOrder = [
  'web_config', 'tidy', 'webdav', 'cookie_cloud', 'additional_config', 'proxy',
  'log', 'telegram', 'lrc_api', 'ai', 'n8n_config', 'qq_music_api',
]
const sectionLabels: Record<string, string> = {
  web_config: 'Web 服务', tidy: '资源整理', webdav: 'WebDAV', cookie_cloud: 'CookieCloud',
  additional_config: '附加功能', proxy: '代理', log: '日志', telegram: 'Telegram',
  lrc_api: 'LrcAPI', ai: 'AI', n8n_config: 'n8n', qq_music_api: 'QQ 音乐 API',
}
const sectionKeys = computed(() => sectionOrder.filter((key) => !!config.value && config.value[key] && typeof config.value[key] === 'object'))
function sectionLabel(key: string) { return sectionLabels[key] || key }

async function load() {
  loading.value = true
  try {
    cookieStatusLoading.value = true
    cookieStatusFailed.value = false
    void getCookieCloudStatus().then((statusResponse) => {
      if (statusResponse.code === 200 && statusResponse.data) cc.value = statusResponse.data
      else cookieStatusFailed.value = true
    }).catch(() => { cookieStatusFailed.value = true }).finally(() => { cookieStatusLoading.value = false })
    void getSystemStatus().then((statusResponse) => {
      restartSupported.value = !!statusResponse.data?.restart_supported
    }).catch(() => { restartSupported.value = false })
    const configResponse = await getSystemConfig()
    if (configResponse.code === 200 && configResponse.data?.config) {
      config.value = structuredClone(configResponse.data.config)
      if (!sectionKeys.value.includes(activeSection.value)) activeSection.value = sectionKeys.value[0] || ''
    } else message.error(configResponse.message || '读取配置失败')
  } catch (error) {
    message.error(error instanceof Error ? `读取设置失败：${error.message}` : '读取设置失败')
  } finally {
    loading.value = false
  }
}

async function handleSaveConfig() {
  if (!config.value) return
  saving.value = true
  try {
    const response = await updateSystemConfig(config.value, clearSecrets.value)
    if (response.code === 200 && response.data) {
      config.value = structuredClone(response.data.config)
      clearSecrets.value = []
      restartFields.value = response.data.restart_fields || []
      hotReloadFields.value = response.data.hot_reload_fields || []
      message.success('配置已安全写入 config.yaml')
    } else message.error(response.message || '保存失败')
  } catch (error) {
    message.error(error instanceof Error ? `保存失败：${error.message}` : '保存失败')
  } finally {
    saving.value = false
  }
}

async function handleRestart() {
  restarting.value = true
  try {
    const response = await requestApplicationRestart()
    if (response.code === 200) {
      message.info('已发送重启请求，页面稍后会断开；请等待容器重新启动')
    } else {
      message.error(response.message || '重启请求失败')
    }
  } catch (error) {
    message.error(error instanceof Error ? `重启请求失败：${error.message}` : '重启请求失败')
  } finally {
    restarting.value = false
  }
}

async function handleSync() {
  syncing.value = true
  try {
    const response = await syncCookieCloud()
    if (response.code === 200) {
      message.success('同步已触发')
      cookieStatusLoading.value = true
      try {
        const status = await getCookieCloudStatus()
        if (status.code === 200 && status.data) {
          cc.value = status.data
          cookieStatusFailed.value = false
        } else {
          cookieStatusFailed.value = true
        }
      } catch {
        cookieStatusFailed.value = true
      } finally {
        cookieStatusLoading.value = false
      }
    } else message.error(response.message || '同步失败')
  } catch (error) {
    message.error(error instanceof Error ? `同步失败：${error.message}` : '同步失败')
  } finally {
    syncing.value = false
  }
}

onMounted(() => void load())
</script>

<style scoped>
.settings-page { max-width: 1480px; margin: 0 auto; }
.page-heading { display: flex; justify-content: space-between; align-items: flex-end; margin-bottom: 24px; }
.eyebrow { margin: 0 0 7px; color: #638078; font-size: 10px; font-weight: 700; letter-spacing: .19em; }
.page-heading :deep(.n-h2) { margin: 0; }
.settings-layout { min-height: 540px; }
.settings-tabs { min-height: 540px; }
.settings-tabs :deep(.n-tabs-nav) { width: 185px; }
.section-card { min-height: 500px; background: rgba(20,24,29,.65); }
.section-help { margin: 0 0 20px; color: #75817b; font-size: 11px; line-height: 1.7; }
.restart-alert { margin-bottom: 14px; }
.cookie-card { margin-top: 20px; background: rgba(20,24,29,.65); }
</style>
