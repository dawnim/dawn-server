<template>
  <CrudPage
    resource="/business/@@table@@"
    permission="app.@@table@@"
    :api="api"
    :columns="columns"
    :search="search"
    :defaults="defaults"
  />
</template>
<script setup lang="ts">
  import { computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { CrudPage } from '@dawnim/admin'
  import api, { columns as getColumns, search, defaults } from '../../../api/business/@@table@@'
  import zh from '../../../locales/zh/@@table@@'
  import en from '../../../locales/en/@@table@@'
  const { t } = useI18n({ useScope: 'local', messages: { zh, en }, fallbackFormat: true, missingWarn: false, fallbackWarn: false })
  const columns = computed(() => getColumns(t))
</script>
