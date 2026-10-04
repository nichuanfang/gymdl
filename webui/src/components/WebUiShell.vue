<template>
  <TaskNotificationBridge />
  <main v-if="authLoading" class="auth-loading"><span class="auth-loader-mark">♫ GYMDL</span></main>
  <main v-else-if="authSessionUnavailable" class="login-screen">
    <section class="login-card" aria-labelledby="session-unavailable-title">
      <div class="login-mark">GYMDL <span>WEB CONSOLE</span></div>
      <p class="login-kicker">CONNECTION / SESSION CHECK</p>
      <h1 id="session-unavailable-title">暂时无法确认登录状态</h1>
      <p class="login-copy">为保护控制台，登录状态未确认前不会显示管理页面。请检查服务连接后重试。</p>
      <n-button class="login-submit" type="primary" block size="large" :loading="authLoading" @click="refreshSession">
        重试连接
      </n-button>
    </section>
  </main>
  <main v-else-if="authRequired && !authenticated" class="login-screen">
    <section class="login-card">
      <div class="login-mark">GYMDL <span>WEB CONSOLE</span></div>
      <p class="login-kicker">PRIVATE MUSIC LIBRARY</p>
      <h1>欢迎回来</h1>
      <p class="login-copy">登录后继续管理搜索、下载任务与整理文件。</p>
      <n-form @submit.prevent="handleLogin">
        <n-form-item label="用户名">
          <n-input v-model:value="username" autocomplete="username" placeholder="输入管理员用户名" @keyup.enter="handleLogin" />
        </n-form-item>
        <n-form-item label="密码">
          <n-input v-model:value="password" type="password" show-password-on="click" autocomplete="current-password" placeholder="输入密码" @keyup.enter="handleLogin" />
        </n-form-item>
        <n-button class="login-submit" type="primary" block size="large" :loading="loggingIn" @click="handleLogin">
          登录控制台
        </n-button>
      </n-form>
      <p class="login-footnote">仅供已授权的管理员访问</p>
    </section>
  </main>
  <n-layout v-else class="app-shell" has-sider>
    <n-layout-sider bordered :width="220" :collapsed-width="64" :collapsed="isCompactLayout" collapse-mode="width" class="app-sider">
      <div class="brand-lockup"><span class="brand-note">♫</span><span>GYMDL</span></div>
      <n-menu :options="menuOptions" :value="activeKey" @update:value="handleMenuClick" />
      <div class="sider-footer">
        <div class="session-indicator"><span />{{ authRequired ? '已安全连接' : '本地控制台' }}</div>
        <n-button v-if="authRequired" quaternary size="small" @click="handleLogout">退出登录</n-button>
      </div>
    </n-layout-sider>
    <n-layout-content class="content-shell">
      <router-view v-slot="{ Component }">
        <KeepAlive include="Search,History,Files,Settings">
          <component :is="Component" />
        </KeepAlive>
      </router-view>
    </n-layout-content>
  </n-layout>
</template>

<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton, NForm, NFormItem, NInput, NLayout, NLayoutContent, NLayoutSider, NMenu,
  useMessage,
} from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import {
  HomeOutline, DownloadOutline, TimeOutline, FolderOpenOutline, SettingsOutline,
  DocumentTextOutline, ChatboxEllipsesOutline, SearchOutline,
} from '@vicons/ionicons5'
import { NIcon } from 'naive-ui'
import { getAuthSession, login, logout } from '../api'
import TaskNotificationBridge from './TaskNotificationBridge.vue'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const authLoading = ref(true)
const authRequired = ref(false)
const authenticated = ref(true)
const authSessionUnavailable = ref(false)
const loggingIn = ref(false)
const username = ref('')
const password = ref('')
const isCompactLayout = ref(false)
const compactQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 760px)') : null

function updateCompactLayout() {
  isCompactLayout.value = compactQuery?.matches ?? false
}

function renderIcon(icon: any) {
  return () => h(NIcon, null, { default: () => h(icon) })
}

const menuOptions: MenuOption[] = [
  { label: '仪表盘', key: '/', icon: renderIcon(HomeOutline) },
  { label: '搜索', key: '/search', icon: renderIcon(SearchOutline) },
  { label: '下载', key: '/download', icon: renderIcon(DownloadOutline) },
  { label: '历史', key: '/history', icon: renderIcon(TimeOutline) },
  { label: '文件库', key: '/files', icon: renderIcon(FolderOpenOutline) },
  { label: '设置', key: '/settings', icon: renderIcon(SettingsOutline) },
  { label: '日志', key: '/logs', icon: renderIcon(DocumentTextOutline) },
  { label: 'QQ 登录', key: '/qqlogin', icon: renderIcon(ChatboxEllipsesOutline) },
]
const activeKey = computed(() => route.path)

function handleMenuClick(key: string) {
  router.push(key)
}

async function refreshSession() {
  authLoading.value = true
  try {
    const response = await getAuthSession()
    if (response.code !== 200 || !response.data) {
      throw new Error(response.message || '登录状态接口返回异常')
    }
    authRequired.value = !!response.data.auth_enabled
    authenticated.value = !authRequired.value || !!response.data.authenticated
    authSessionUnavailable.value = false
  } catch {
    // Fail closed: do not render the console until the auth mode is known.
    authRequired.value = true
    authenticated.value = false
    authSessionUnavailable.value = true
  } finally {
    authLoading.value = false
  }
}

async function handleLogin() {
  if (!username.value.trim() || !password.value) {
    message.warning('请输入用户名和密码')
    return
  }
  loggingIn.value = true
  try {
    const response = await login(username.value.trim(), password.value)
    if (response.code === 200 && response.data?.authenticated) {
      authenticated.value = true
      password.value = ''
      message.success('登录成功')
    } else {
      message.error(response.message || '登录失败')
    }
  } finally {
    loggingIn.value = false
  }
}

async function handleLogout() {
  await logout()
  authenticated.value = false
  username.value = ''
  password.value = ''
  message.success('已退出登录')
}

function handleUnauthorized() {
  if (!authRequired.value) return
  authenticated.value = false
  message.warning('登录已失效，请重新登录')
}

onMounted(() => {
  updateCompactLayout()
  compactQuery?.addEventListener('change', updateCompactLayout)
  refreshSession()
  window.addEventListener('gymdl:unauthorized', handleUnauthorized)
})
onUnmounted(() => {
  compactQuery?.removeEventListener('change', updateCompactLayout)
  window.removeEventListener('gymdl:unauthorized', handleUnauthorized)
})
</script>

<style scoped>
.app-shell { height: 100vh; background: #0c0e12; }
.app-sider { position: relative; background: #11141a; }
.brand-lockup { padding: 24px 20px 30px; display: flex; align-items: center; gap: 10px; color: #edf2f3; font-size: 17px; letter-spacing: .16em; font-weight: 700; }
.brand-note { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 9px; color: #06140f; background: #63e2b7; font-size: 20px; }
.sider-footer { position: absolute; right: 14px; bottom: 18px; left: 14px; display: grid; gap: 8px; color: #8b959c; font-size: 11px; }
.session-indicator { display: flex; align-items: center; gap: 8px; padding: 8px; border-top: 1px solid rgba(255,255,255,.08); }
.session-indicator span { width: 7px; height: 7px; border-radius: 50%; background: #63e2b7; box-shadow: 0 0 10px rgba(99,226,183,.7); }
.content-shell { overflow: auto; padding: 30px clamp(20px, 3vw, 48px); }
.login-screen { min-height: 100vh; display: grid; place-items: center; padding: 24px; background: radial-gradient(ellipse at 50% 35%, rgba(44, 81, 73, .22), transparent 52%), #0a0c10; }
.login-card { width: min(100%, 410px); padding: 36px; border: 1px solid rgba(141, 167, 160, .18); border-radius: 18px; background: rgba(20, 24, 30, .94); box-shadow: 0 30px 100px rgba(0,0,0,.45); }
.login-mark { color: #edf2f3; font-size: 14px; font-weight: 700; letter-spacing: .2em; }
.login-mark span { margin-left: 8px; color: #82918d; font-size: 9px; letter-spacing: .16em; }
.login-kicker { margin: 36px 0 8px; color: #63e2b7; font-size: 10px; font-weight: 700; letter-spacing: .22em; }
.login-card h1 { margin: 0; color: #f2f6f4; font-size: 28px; font-weight: 600; }
.login-copy { margin: 10px 0 28px; color: #929ca1; font-size: 13px; line-height: 1.7; }
.login-submit { margin-top: 8px; letter-spacing: .06em; }
.login-footnote { margin: 22px 0 0; color: #657076; font-size: 11px; text-align: center; }
.auth-loading { position: fixed; inset: 0; display: grid; place-items: center; background: #0a0c10; }
.auth-loader-mark { color: #63e2b7; font-size: 13px; font-weight: 700; letter-spacing: .2em; animation: breathe 1.3s ease-in-out infinite alternate; }
@keyframes breathe { to { opacity: .42; transform: translateY(-2px); } }
@media (max-width: 760px) {
  .brand-lockup { justify-content: center; padding: 20px 0 22px; }
  .brand-lockup > span:last-child { display: none; }
  .sider-footer { right: 0; bottom: 12px; left: 0; display: flex; justify-content: center; }
  .session-indicator { justify-content: center; padding: 7px 0; border-top: 0; font-size: 0; }
  .content-shell { padding: 20px 14px; }
}
</style>
