<template>
  <header class="page-heading">
    <div class="page-heading-copy">
      <p class="page-eyebrow">{{ eyebrow }}</p>
      <n-h2>{{ title }}</n-h2>
    </div>
    <div v-if="$slots.actions" class="page-heading-actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<script setup lang="ts">
import { NH2 } from 'naive-ui'

defineProps<{
  eyebrow: string
  title: string
}>()
</script>

<style scoped>
.page-heading {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  min-width: 0;
  margin: 0 0 24px;
}
.page-heading-copy { min-width: 0; }
.page-eyebrow {
  margin: 0 0 7px;
  color: #638078;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: .19em;
  line-height: 1.4;
}
.page-heading-copy :deep(.n-h2) {
  margin: 0;
  color: #edf2f0;
}
.page-heading-actions {
  display: flex;
  flex: 0 1 auto;
  align-items: center;
  justify-content: flex-end;
  min-width: 0;
}
@media (max-width: 600px) {
  .page-heading {
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 12px;
  }
  .page-heading-actions {
    width: 100%;
    justify-content: flex-start;
  }
}
</style>
