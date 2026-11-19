<template>
  <DawnSearchBar
    ref="searchBarRef"
    v-model="formData"
    label-width="100px"
    :show-expand="columns.length > 3"
    @expand="expanded = $event"
    @reset="reset"
    @search="emit('search', formData)"
  >
    <ElCol
      v-for="column in columns.slice(0, expanded ? undefined : 3)"
      :key="column.prop"
      :span="6"
      :xs="24"
      :sm="12"
      :md="8"
      :lg="6"
      :xl="6"
    >
      <ElFormItem :label="column.label" :prop="column.prop">
        <DawnSelect
          v-if="column.dict"
          v-model="formData[column.prop]"
          :dict="column.dict"
          :value-type="column.dictValueType || 'number'"
          clearable
        />
        <ElSelect v-else-if="column.options" v-model="formData[column.prop]" clearable>
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
  import { DawnSearchBar, DawnSelect, type Column } from '@dawnim/admin'
  const { t } = useI18n()
  const props = defineProps<{ modelValue: Record<string, any>; columns: Column[] }>()
  const emit = defineEmits<{
    'update:modelValue': [Record<string, any>]
    search: [Record<string, any>]
    reset: []
  }>()
  const expanded = ref(false)
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
