<template>
  <div class="art-full-height">
    <TableSearch
      v-if="searchColumns.length"
      v-model="searchForm"
      :columns="searchColumns"
      @search="handleSearch"
      @reset="resetSearchParams"
    />
    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader v-model:columns="columnChecks" :loading="loading" @refresh="refreshData">
        <template #left>
          <ElSpace wrap>
            <ElButton v-if="editable && can('create')" @click="showDialog('add')" v-ripple
              ><template #icon><ArtSvgIcon icon="ri:add-fill" /></template
              >{{ t('新增') }}</ElButton
            >
            <ElButton
              v-if="editable && can('delete')"
              :disabled="selectedRows.length === 0"
              @click="deleteSelectedRows(api.delete, changed)"
              v-ripple
              ><template #icon><ArtSvgIcon icon="ri:delete-bin-5-line" /></template
              >{{ t('删除') }}</ElButton
            >
            <DawnImport
              v-if="importable && can('create')"
              :download-url="resource + '/template'"
              accept=".csv"
              :upload-url="resource + '/import'"
              @success="changed"
            />
            <DawnExport
              v-if="exportable && can('export')"
              :url="resource + '/export'"
              method="get"
              file-name="export.csv"
            />
            <slot name="toolbar" :reload="refreshData" />
          </ElSpace>
        </template>
      </ArtTableHeader>
      <ArtTable
        row-key="id"
        :loading="loading"
        :data="data"
        :columns="tableColumns"
        :pagination="pagination"
        @sort-change="handleSortChange"
        @selection-change="handleSelectionChange"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      >
        <template #operation="{ row }"
          ><div class="flex gap-2">
            <DawnButton
              v-if="editable && can('update')"
              type="secondary"
              @click="showDialog('edit', row)" />
            <DawnButton
              v-if="editable && can('delete')"
              type="error"
              @click="deleteRow(row, api.delete, changed)" />
            <slot name="actions" :row="row" :reload="refreshData" /></div
        ></template>
      </ArtTable>
    </ElCard>
    <EditDialog
      v-model="dialogVisible"
      :dialog-type="dialogType"
      :data="dialogData"
      :columns="columns"
      :defaults="defaults"
      :api="api"
      @success="changed"
      ><template #form="scope"><slot name="form" v-bind="scope" /></template
    ></EditDialog>
  </div>
</template>
<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { ElCard, ElSpace, ElButton } from 'element-plus'
  import { dateLabel } from '@dawnim/admin'
  import TableSearch from './modules/table-search.vue'
  import EditDialog from './modules/edit-dialog.vue'
  import {
    ArtTable,
    ArtTableHeader,
    ArtSvgIcon,
    DawnButton,
    DawnImport,
    DawnExport,
    useTable,
    useAdmin,
    useAuth
  } from '@dawnim/admin'
  import api, { columns as getColumns, search, defaults } from '../../../api/business/@@table@@'
  import zh from '../../../locales/zh/@@table@@'
  import en from '../../../locales/en/@@table@@'
  const { t } = useI18n({ useScope: 'global' })
  const { t: fieldText } = useI18n({ useScope: 'local', messages: { zh, en }, fallbackFormat: true, missingWarn: false, fallbackWarn: false })
  const { hasAuth } = useAuth()
  const columns = computed(() => getColumns(fieldText))
  const resource = '/business/@@table@@',
    permission = 'app.@@table@@'
  const editable = true,
    exportable = false,
    importable = false
  const emit = defineEmits<{ change: [] }>()
  const searchForm = ref<Record<string, any>>(
    Object.fromEntries(search.map((field) => [field, undefined]))
  )
  const searchColumns = computed(() =>
    columns.value.filter((column) => search.includes(column.prop))
  )
  const can = (action: string) => !permission || hasAuth(permission + '.' + action)
  const {
    columns: tableColumns,
    columnChecks,
    data,
    loading,
    getData,
    searchParams,
    pagination,
    resetSearchParams,
    handleSortChange,
    handleSizeChange,
    handleCurrentChange,
    refreshData
  } = useTable({
    core: {
      apiFn: api.list,
      columnsFactory: () => [
        { type: 'selection' },
        { prop: 'id', label: 'ID', width: 100, align: 'center', sortable: true },
        ...columns.value
          .filter((column) => !column.hidden && column.prop !== 'id')
          .map((column) => ({
            prop: column.prop,
            label: column.label,
            minWidth: 130,
            sortable: true,
            showOverflowTooltip: true,
            formatter:
              column.type === 'date'
                ? (row: Record<string, any>) => dateLabel(row[column.prop])
                : column.options
                  ? (row: Record<string, any>) =>
                      column.options?.find((option) => option.value === row[column.prop])?.label ??
                      row[column.prop]
                  : undefined
          })),
        {
          prop: 'operation',
          label: t('操作'),
          width: 100,
          fixed: 'right',
          useSlot: true
        }
      ]
    },
    hooks: {
      resetFormCallback: () =>
        Object.keys(searchForm.value).forEach((key) => (searchForm.value[key] = undefined))
    }
  })
  const {
    dialogType,
    dialogVisible,
    dialogData,
    showDialog,
    deleteRow,
    deleteSelectedRows,
    handleSelectionChange,
    selectedRows
  } = useAdmin()
  function handleSearch(params: Record<string, any>) {
    Object.assign(searchParams, params)
    getData()
  }
  function changed() {
    refreshData()
    emit('change')
  }
  defineExpose({ load: refreshData })
</script>
