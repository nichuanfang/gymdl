<template>
  <n-space vertical :size="10">
    <template v-for="[key, value] of editableEntries" :key="`${path}.${key}`">
      <n-card v-if="isObject(value)" size="small" class="nested-config" :bordered="false">
        <template #header>{{ labelFor(key) }}</template>
        <ConfigEditorFields
          :model-value="value as Record<string, any>"
          :path="joinPath(key)"
          :clear-secrets="clearSecrets"
          @update:model-value="setField(key, $event)"
          @update:clear-secrets="emit('update:clearSecrets', $event)"
        />
      </n-card>

      <n-form-item
        v-else
        :label="labelFor(key)"
        :show-feedback="!!arrayErrors[joinPath(key)]"
        :feedback="arrayErrors[joinPath(key)]"
        :validation-status="arrayErrors[joinPath(key)] ? 'error' : undefined"
        class="config-field"
      >
        <n-space v-if="isSecret(key)" align="center" :size="8" class="secret-row">
          <n-input
            :value="typeof value === 'string' ? value : ''"
            type="password"
            show-password-on="click"
            :placeholder="modelValue[`${key}_configured`] ? '已配置，留空表示不修改' : '未配置'"
            @update:value="setField(key, $event)"
          />
          <n-button
            v-if="modelValue[`${key}_configured`] || hasValue(value)"
            size="small"
            quaternary
            type="error"
            @click="clearSecret(key)"
          >清除</n-button>
        </n-space>
        <n-select
          v-else-if="selectOptions(joinPath(key))"
          :value="value"
          :options="selectOptions(joinPath(key))"
          placeholder="请选择"
          @update:value="setField(key, $event)"
        />
        <n-checkbox v-else-if="typeof value === 'boolean'" :checked="value" @update:checked="setField(key, $event)">
          {{ value ? '启用' : '关闭' }}
        </n-checkbox>
        <n-input-number
          v-else-if="typeof value === 'number'"
          :value="value"
          :show-button="false"
          @update:value="setField(key, $event ?? 0)"
        />
        <n-input
          v-else-if="Array.isArray(value) && isStringList(key)"
          :value="formatStringList(value)"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 6 }"
          placeholder="每行一项"
          @update:value="setField(key, parseStringList($event))"
        />
        <n-input
          v-else-if="Array.isArray(value)"
          :value="arrayValue(key, value)"
          type="textarea"
          :autosize="{ minRows: 3, maxRows: 10 }"
          placeholder="使用 JSON 数组格式"
          @update:value="updateArray(key, $event)"
        />
        <n-input
          v-else
          :value="value == null ? '' : String(value)"
          :type="isLongText(key) ? 'textarea' : 'text'"
          :autosize="isLongText(key) ? { minRows: 3, maxRows: 8 } : undefined"
          @update:value="setField(key, $event)"
        />
      </n-form-item>
    </template>
  </n-space>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NCard, NCheckbox, NFormItem, NInput, NInputNumber, NSelect, NSpace } from 'naive-ui'

defineOptions({ name: 'ConfigEditorFields' })
const props = defineProps<{
  modelValue: Record<string, any>
  path: string
  clearSecrets: string[]
}>()
const emit = defineEmits<{
  'update:modelValue': [value: Record<string, any>]
  'update:clearSecrets': [value: string[]]
}>()

const secretKeys = new Set([
  'webdav_pass', 'cookiecloud_uuid', 'cookiecloud_key', 'bot_token', 'api_key',
  'auth_token', 'webhook_url', 'proxy_pass', 'pass', 'password', 'token', 'secret', 'user', 'refresh_key', 'refresh_token',
  'access_token', 'music_id', 'str_music_id', 'open_id', 'union_id', 'music_key',
])
const labels: Record<string, string> = {
  enable: '启用', app_domain: '访问域名', https: '启用 HTTPS', app_port: '监听端口', gin_mode: '运行模式',
  mode: '模式', tidy: '资源整理', dist_dir: '本地整理目录', webdav_url: 'WebDAV 地址', webdav_user: 'WebDAV 用户名',
  webdav_pass: 'WebDAV 密码', webdav_dir: 'WebDAV 目录', webdav_host: 'WebDAV Host', cookiecloud_url: '服务地址',
  cookiecloud_uuid: 'UUID', cookiecloud_key: '加密密钥', cookie_file: 'Cookie 文件名', cookie_file_path: 'Cookie 目录',
  expire_time: '过期时间（分钟）', enable_cron: '启用定时任务', enable_monitor: '启用目录监听', monitor_dirs: '监听目录',
  music_mode: '视频链接转音乐', scheme: '代理协议', host: '代理主机', port: '代理端口', user: '代理用户名', pass: '代理密码', proxy: '代理',
  web_config: 'Web 服务', auth: 'WebUI 管理登录', username: '管理员用户名', password: '管理员密码',
  cookie_cloud: 'CookieCloud', webdav: 'WebDAV', log: '日志', telegram: 'Telegram',
  lrc_api: 'LrcAPI', ai: 'AI', n8n_config: 'n8n', qq_music_api: 'QQ 音乐 API', additional_config: '附加功能',
  file: '日志文件', level: '日志等级', chat_id: 'Chat ID', allowed_users: '允许的用户', webhook_url: 'Webhook 地址',
  webhook_port: 'Webhook 端口', lrc_api_url: 'LrcAPI 地址', lrc_api_host: 'LrcAPI Host', base_url: '服务地址',
  model: '模型', system_prompt: '系统提示词', n8n_base_url: 'n8n 地址', empty_trash_endpoint: '清空回收站端点',
  ai_playlist_endpoint: 'AI 歌单端点', tidy_playlist_endpoint: '歌单整理端点', playlist_assist: '歌单分类增强',
  tidy_playlist: '整理歌单', endpoint: 'API 地址', proxy_url: '下载代理', vip_level: '音质等级', login_type: '登录方式',
}

const enumOptions: Record<string, Array<{ label: string; value: string | number }>> = {
  'web_config.gin_mode': [
    { label: 'Debug（开发）', value: 'debug' }, { label: 'Release（生产）', value: 'release' }, { label: 'Test（测试）', value: 'test' },
  ],
  'tidy.mode': [{ label: '本地目录', value: 1 }, { label: 'WebDAV', value: 2 }],
  'cookie_cloud.mode': [{ label: '定时同步', value: 1 }, { label: 'Webhook', value: 2 }],
  'proxy.scheme': [{ label: 'HTTP', value: 'http' }, { label: 'SOCKS5', value: 'socks5' }],
  'log.mode': [{ label: '标准输出', value: 1 }, { label: '日志文件', value: 2 }, { label: '标准输出和文件', value: 3 }],
  'log.level': [
    { label: 'Debug', value: 1 }, { label: 'Info', value: 2 }, { label: 'Warn', value: 3 },
    { label: 'Error', value: 4 }, { label: 'Fatal', value: 5 },
  ],
  'telegram.mode': [{ label: '长轮询', value: 1 }, { label: 'Webhook', value: 2 }],
  'qq_music_api.login_type': [{ label: 'QQ', value: 0 }, { label: '微信', value: 1 }, { label: '手机号', value: 2 }],
  'qq_music_api.vip_level': [{ label: '使用默认音质', value: '' }, { label: 'VIP', value: 'vip' }, { label: 'SVIP', value: 'svip' }],
}
const arrayDrafts = ref<Record<string, string>>({})
const arrayErrors = ref<Record<string, string>>({})

const editableEntries = computed(() => Object.entries(props.modelValue).filter(([key]) => !key.endsWith('_configured')))
function isObject(value: unknown): value is Record<string, any> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
function isSecret(key: string) { return secretKeys.has(key.toLowerCase()) }
function hasValue(value: unknown) { return typeof value === 'string' && value.length > 0 }
function labelFor(key: string) { return labels[key] || key.replaceAll('_', ' ') }
function joinPath(key: string) { return props.path ? `${props.path}.${key}` : key }
function formatArray(value: unknown[]) { return JSON.stringify(value, null, 2) }
function selectOptions(path: string) { return enumOptions[path] }
function isStringList(key: string) { return key === 'monitor_dirs' || key === 'allowed_users' }
function formatStringList(value: unknown[]) { return value.map(String).join('\n') }
function parseStringList(value: string) { return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean) }
function arrayValue(key: string, value: unknown[]) {
  const path = joinPath(key)
  return arrayDrafts.value[path] ?? formatArray(value)
}
function isLongText(key: string) { return ['system_prompt'].includes(key) }
function setField(key: string, value: unknown) {
  const next = { ...props.modelValue, [key]: value }
  emit('update:modelValue', next)
  if (isSecret(key) && hasValue(value)) {
    emit('update:clearSecrets', props.clearSecrets.filter((path) => path !== joinPath(key)))
  }
}
function clearSecret(key: string) {
  setField(key, '')
  emit('update:clearSecrets', [...new Set([...props.clearSecrets, joinPath(key)])])
}
function updateArray(key: string, value: string) {
  const path = joinPath(key)
  arrayDrafts.value[path] = value
  try {
    const parsed = JSON.parse(value)
    if (!Array.isArray(parsed)) throw new Error('not an array')
    delete arrayDrafts.value[path]
    delete arrayErrors.value[path]
    setField(key, parsed)
  } catch {
    arrayErrors.value[path] = 'JSON 数组格式无效；当前仍保留上次有效值'
  }
}
</script>

<style scoped>
.nested-config { background: rgba(255,255,255,.018); border: 1px solid rgba(255,255,255,.055); }
.nested-config :deep(.n-card-header) { padding: 12px 14px 8px; color: #85a399; font-size: 11px; }
.nested-config :deep(.n-card__content) { padding: 0 14px 14px; }
.config-field { max-width: 700px; margin-bottom: 6px; }
.config-field :deep(.n-form-item-label) { color: #b2beb8; font-size: 11px; }
.config-field :deep(.n-input), .config-field :deep(.n-input-number) { width: min(100%, 480px); }
.secret-row { width: min(100%, 550px); }
.secret-row :deep(.n-input) { flex: 1; }
</style>
