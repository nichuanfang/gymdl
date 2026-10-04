<template>
  <section class="qq-page">
    <header class="page-heading">
      <div>
        <p class="eyebrow">ACCOUNT / CREDENTIAL STATUS</p>
        <n-h2>QQ 音乐登录</n-h2>
      </div>
      <n-button quaternary :loading="checking" @click="loadOverview">刷新状态</n-button>
    </header>

    <n-alert v-if="overview && !overview.enabled" type="warning" :show-icon="false">
      QQ Music API 未启用，请在配置中填写服务地址并启用后再登录。
    </n-alert>

    <n-card v-else-if="!qrcode && overview?.status === 'logged_in' && !forceQRCode" class="status-card" :bordered="false">
      <div class="status-orbit"><span>✓</span></div>
      <p class="eyebrow">CREDENTIAL READY</p>
      <h3>QQ 音乐已登录</h3>
      <n-tag v-if="overview.account" type="success" :bordered="false">账号 {{ overview.account }}</n-tag>
      <p class="status-copy">凭证状态正常，可用于 QQ 音乐搜索和下载。</p>
      <n-button secondary @click="forceQRCode = true">重新登录</n-button>
    </n-card>

    <n-card v-else-if="!qrcode && overview" class="login-card" :bordered="false">
      <template v-if="overview.status === 'unknown'">
        <n-alert type="warning" :show-icon="false" style="margin-bottom: 18px">
          {{ overview.message || '暂时无法确认 QQ 登录状态' }}，可以重试查询或重新登录。
        </n-alert>
      </template>
      <n-alert
        v-else-if="overview.status === 'logged_out' && overview.message"
        type="warning"
        :show-icon="false"
        style="margin-bottom: 18px"
      >
        {{ overview.message }}
      </n-alert>
      <div class="qr-mark">♫</div>
      <p class="eyebrow">SCAN TO CONNECT</p>
      <h3>{{ overview.status === 'logged_out' ? (overview.account ? '登录凭证已失效' : '尚未登录') : '连接 QQ 音乐' }}</h3>
      <p class="status-copy">使用 QQ 或微信扫描二维码。登录成功后凭证会安全保存到本地 config.yaml，页面不会展示密钥。</p>
      <n-button type="primary" size="large" :loading="starting" @click="startLogin">获取登录二维码</n-button>
    </n-card>

    <n-card v-else-if="qrcode" class="login-card" :bordered="false">
      <p class="eyebrow">SCAN TO CONNECT</p>
      <h3>{{ statusText }}</h3>
      <img
        v-if="status !== 'done'"
        :src="'data:image/png;base64,' + qrcode.image_base64"
        class="qr-image"
        :class="{ faded: ['timeout', 'refused', 'error'].includes(status) }"
        alt="QQ 音乐登录二维码"
      />
      <n-alert v-if="status === 'done'" type="success" :show-icon="false" style="margin: 18px 0">
        登录流程已完成，正在确认凭证状态…
      </n-alert>
      <p v-else class="status-copy">{{ qrcode.message || '请使用 QQ 或微信扫码，并在手机上确认。' }}</p>
      <n-space justify="center">
        <n-button v-if="status === 'done'" type="primary" @click="finishLogin">完成</n-button>
        <n-button v-else-if="['timeout', 'refused', 'error'].includes(status)" @click="reset">重新获取</n-button>
        <n-button v-else quaternary type="error" @click="cancel">取消</n-button>
      </n-space>
    </n-card>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NH2, NButton, NCard, NSpace, NTag, NAlert, useMessage } from 'naive-ui'
import { post, get, del } from '../api/http'
import { getQQLoginOverview } from '../api'
import type { QQLoginOverview } from '../api'

interface QQLoginState {
  identifier: string
  image_base64: string
  status: string
  message?: string
}
const message = useMessage()
const overview = ref<QQLoginOverview | null>(null)
const checking = ref(false)
const starting = ref(false)
const qrcode = ref<QQLoginState | null>(null)
const forceQRCode = ref(false)
let pollTimer: ReturnType<typeof setInterval> | null = null

const status = computed(() => qrcode.value?.status || '')
const statusText = computed(() => {
  switch (status.value) {
    case 'waiting_scan': return '等待扫码'
    case 'scanned': return '已扫码，请确认登录'
    case 'done': return '登录成功'
    case 'timeout': return '二维码已过期'
    case 'refused': return '登录已取消'
    case 'error': return '暂时无法查询登录状态'
    default: return '正在获取二维码'
  }
})

async function loadOverview() {
  checking.value = true
  try {
    const response = await getQQLoginOverview()
    if (response.code === 200 && response.data) overview.value = response.data
    else message.error(response.message || '读取 QQ 登录状态失败')
  } catch {
    overview.value = { enabled: true, status: 'unknown', message: '无法连接 QQ Music API' }
  } finally {
    checking.value = false
  }
}
async function startLogin() {
  starting.value = true
  try {
    const response = await post<{ identifier: string; image_base64: string }>('/qqmusic/qrcode')
    if (response.code === 200 && response.data) {
      qrcode.value = { ...response.data, status: 'waiting_scan' }
      forceQRCode.value = false
      startPolling()
    } else message.error(response.message || '获取二维码失败')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '获取二维码失败')
  } finally {
    starting.value = false
  }
}
function startPolling() {
  stopPolling()
  pollTimer = setInterval(async () => {
    if (!qrcode.value) return
    try {
      const response = await get<QQLoginState>(`/qqmusic/qrcode/${qrcode.value.identifier}`)
      if (response.code === 200 && response.data) {
        qrcode.value = response.data
        if (['done', 'timeout', 'refused', 'error'].includes(response.data.status)) {
          stopPolling()
          if (response.data.status === 'done') await loadOverview()
        }
      } else {
        qrcode.value.status = 'error'
        qrcode.value.message = response.message || '查询登录状态失败，请重试'
        stopPolling()
      }
    } catch {
      qrcode.value.status = 'error'
      qrcode.value.message = '查询登录状态失败，请重试'
      stopPolling()
    }
  }, 2000)
}
function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
}
async function cancel() {
  stopPolling()
  try {
    if (qrcode.value) await del(`/qqmusic/qrcode/${qrcode.value.identifier}`)
  } catch {
    // Cancelling the local QR view should not be blocked by a transient API error.
  }
  reset()
}
function reset() {
  stopPolling()
  qrcode.value = null
  forceQRCode.value = false
}
async function finishLogin() {
  qrcode.value = null
  forceQRCode.value = false
  await loadOverview()
  if (overview.value?.status === 'logged_in') message.success('QQ 音乐登录状态已更新')
  else message.warning('登录流程完成，但凭证状态尚未确认，请刷新状态')
}

onMounted(() => void loadOverview())
onUnmounted(stopPolling)
</script>

<style scoped>
.qq-page { max-width: 1000px; margin: 0 auto; }
.page-heading { display: flex; justify-content: space-between; align-items: flex-end; margin-bottom: 24px; }
.eyebrow { margin: 0 0 8px; color: #638078; font-size: 10px; font-weight: 700; letter-spacing: .19em; }
.page-heading :deep(.n-h2) { margin: 0; }
.login-card, .status-card { width: min(100%, 560px); margin: 10vh auto 0; padding: 28px 30px; border: 1px solid rgba(141,167,160,.12); background: linear-gradient(150deg, rgba(24,29,34,.98), rgba(17,20,25,.98)); text-align: center; }
.login-card h3, .status-card h3 { margin: 0 0 10px; color: #edf3f0; font-size: 20px; font-weight: 600; }
.status-copy { max-width: 390px; margin: 12px auto 22px; color: #87938e; font-size: 12px; line-height: 1.75; }
.qr-mark, .status-orbit { display: grid; place-items: center; width: 84px; height: 84px; margin: 0 auto 20px; border: 1px solid rgba(99,226,183,.24); border-radius: 24px; color: #63e2b7; background: radial-gradient(circle at 35% 25%, rgba(99,226,183,.18), rgba(25,35,33,.65)); font-size: 38px; }
.status-orbit { border-radius: 50%; font-size: 32px; box-shadow: 0 0 32px rgba(99,226,183,.09); }
.qr-image { display: block; width: 220px; height: 220px; margin: 20px auto; padding: 10px; border-radius: 12px; background: white; }
.qr-image.faded { opacity: .35; }
</style>
