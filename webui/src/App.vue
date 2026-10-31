<template>
  <n-config-provider :theme="darkTheme" :locale="zhCN" :date-locale="dateZhCN" style="height: 100vh">
    <n-message-provider placement="bottom" :max="4">
      <n-dialog-provider>
        <WebUiShell />
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider, NDialogProvider, darkTheme, zhCN, dateZhCN } from 'naive-ui'
import WebUiShell from './components/WebUiShell.vue'
</script>

<style>
html, body, #app { margin: 0; padding: 0; min-height: 100%; background: #0c0e12; }
body { color: #e8eeed; -webkit-font-smoothing: antialiased; }
::selection { background: rgba(99,226,183,.28); color: #f3fbf7; }
::-webkit-scrollbar { width: 10px; height: 10px; }
::-webkit-scrollbar-thumb { border: 3px solid transparent; border-radius: 999px; background: rgba(125,150,141,.35); background-clip: content-box; }
::-webkit-scrollbar-thumb:hover { background: rgba(125,150,141,.55); background-clip: content-box; }
::-webkit-scrollbar-track { background: transparent; }
</style>
