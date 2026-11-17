<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="themeOverrides" :locale="zhCN" :date-locale="dateZhCN" style="height: 100vh">
    <n-message-provider placement="bottom" :max="4">
      <n-dialog-provider>
        <WebUiShell />
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider, NDialogProvider, darkTheme, zhCN, dateZhCN } from 'naive-ui'
import type { GlobalThemeOverrides } from 'naive-ui'
import WebUiShell from './components/WebUiShell.vue'

const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#63e2b7',
    primaryColorHover: '#8aebc9',
    primaryColorPressed: '#3fbf99',
    primaryColorSuppl: '#63e2b7',
    borderRadius: '12px',
    fontFamily: '"Segoe UI", "PingFang SC", "Noto Sans SC", "Microsoft YaHei", sans-serif',
  },
}
</script>

<style>
:root {
  --glass: rgba(16, 20, 25, .58);
  --glass-line: rgba(186, 206, 198, .16);
  --mint: #63e2b7;
  --ease: cubic-bezier(.22, .8, .32, 1);
}
html, body, #app { margin: 0; padding: 0; min-height: 100%; background: #0b0d11; }
body { color: #e8eeed; -webkit-font-smoothing: antialiased; }
::selection { background: rgba(99,226,183,.28); color: #f3fbf7; }
::-webkit-scrollbar { width: 10px; height: 10px; }
::-webkit-scrollbar-thumb { border: 3px solid transparent; border-radius: 999px; background: rgba(125,150,141,.35); background-clip: content-box; }
::-webkit-scrollbar-thumb:hover { background: rgba(125,150,141,.55); background-clip: content-box; }
::-webkit-scrollbar-track { background: transparent; }

.n-card {
  background: var(--glass) !important;
  backdrop-filter: blur(18px) saturate(1.4);
  -webkit-backdrop-filter: blur(18px) saturate(1.4);
  border: 1px solid var(--glass-line) !important;
  border-radius: 16px !important;
  box-shadow: 0 18px 50px rgba(0, 0, 0, .22), inset 0 1px 0 rgba(255, 255, 255, .05);
}
.n-modal, .n-dialog.n-modal, .n-popover, .n-dropdown-menu {
  backdrop-filter: blur(22px) saturate(1.45);
  -webkit-backdrop-filter: blur(22px) saturate(1.45);
}
.n-data-table { background: transparent !important; }
.n-data-table-th {
  background: rgba(14, 20, 22, .72) !important;
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  color: #9eada8 !important;
  font-weight: 600 !important;
}
.n-data-table-td {
  background: transparent !important;
  transition: background-color .22s var(--ease);
}
.n-data-table-tr:hover > .n-data-table-td { background: rgba(99, 226, 183, .07) !important; }
.n-data-table-td--fixed-left, .n-data-table-td--fixed-right,
.n-data-table-th--fixed-left, .n-data-table-th--fixed-right {
  background: rgba(14, 18, 22, .9) !important;
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
}
.n-data-table-tr:hover > .n-data-table-td--fixed-left,
.n-data-table-tr:hover > .n-data-table-td--fixed-right { background: rgba(20, 36, 32, .94) !important; }
.n-modal-mask, .n-drawer-mask {
  backdrop-filter: blur(10px) saturate(1.15);
  -webkit-backdrop-filter: blur(10px) saturate(1.15);
}
.n-alert { backdrop-filter: blur(16px) saturate(1.25); -webkit-backdrop-filter: blur(16px) saturate(1.25); }
.n-input, .n-base-selection {
  transition: border-color .22s var(--ease), box-shadow .22s var(--ease), background .22s var(--ease);
}
.n-input:focus-within, .n-base-selection--focus {
  box-shadow: 0 0 0 3px rgba(99, 226, 183, .12);
}
.n-button { transition: transform .22s var(--ease), box-shadow .22s var(--ease), background .22s var(--ease); }
.n-button:not(:disabled):hover { transform: translateY(-1px); }
.n-button:not(:disabled):active { transform: translateY(0) scale(.98); }
.n-tag { backdrop-filter: blur(10px); }

@media (prefers-reduced-motion: reduce) {
  .n-button, .n-input, .n-base-selection { transition: none; }
  .n-button:hover, .n-button:active { transform: none; }
}
</style>
