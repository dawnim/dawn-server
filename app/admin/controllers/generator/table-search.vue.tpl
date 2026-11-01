<template>
  <DawnSearchBar
    ref="searchBarRef"
    v-model="formData"
    label-width="100px"
    :show-expand="false"
    @reset="reset"
    @search="emit('search', formData)"
  >
    <ElCol
      v-for="column in columns"
      :key="column.prop"
      :span="6"
      :xs="24"
      :sm="12"
      :md="8"
      :lg="6"
      :xl="6"
    >
      <ElFormItem :label="column.label" :prop="column.prop">
        <ElSelect v-if="column.options" v-model="formData[column.prop]" clearable>
          <ElOption
            v-for="option in column.options"
            :key="option.value"
            :label="option.label"
            :value="option.value"
          />
        </ElSelect>
        <ElInput
          v-else
          v-model="formData[column.prop]"
          :placeholder="t('请填写{label}', { label: column.label })"
          clearable
          @keyup.enter="emit('search', formData)"
        />
      </ElFormItem>
    </ElCol>
  </DawnSearchBar>
</template>
<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { ElCol, ElFormItem, ElInput, ElSelect, ElOption, type FormInstance } from 'element-plus'
  import { DawnSearchBar } from '@dawnim/admin'
  import type { Column } from '@dawnim/admin'
  const { t } = useI18n()
  const props = defineProps<{
    modelValue: Record<string, any>
    columns: Column[]
  }>()
  const emit = defineEmits<{
    'update:modelValue': [Record<string, any>]
    search: [Record<string, any>]
    reset: []
  }>()
  const searchBarRef = ref<{ ref: FormInstance }>()
  const formData = computed({
    get: () => props.modelValue,
    set: (value) => emit('update:modelValue', value)
  })
  function reset() {
    searchBarRef.value?.ref?.resetFields()
    emit('reset')
  }
</script>
