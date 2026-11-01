import { request, type Column, type CrudApi, type PageData } from '@dawnim/admin'

export const columns = (t: (key: string) => string): Column[] =>
  (@@columns@@ as Column[]).map((column) => ({ ...column, label: t(column.label) }))
export const search: string[] = @@search@@
export const defaults: Record<string, any> = @@defaults@@

const path = '/business/@@table@@'
export default {
  list: (params: Record<string, any>) => request.get<PageData>({ url: path, params }),
  read: (id: string | number) => request.get<Record<string, any>>({ url: `${path}/${id}` }),
  save: (data: Record<string, any>) => request.post({ url: path, data }),
  update: (data: Record<string, any>) => request.put({ url: `${path}/${data.id}`, data }),
  delete: ({ ids }: { ids: (string | number)[] }) =>
    Promise.all(ids.map((id) => request.del({ url: `${path}/${id}` })))
} satisfies CrudApi
