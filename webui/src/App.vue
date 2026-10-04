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
body { color: #e8eeed; }
</style>
