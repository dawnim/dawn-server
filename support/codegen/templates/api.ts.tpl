import { createResourceApi, type Column, type ListQuery } from '@dawnim/admin'

export interface @@name@@ {
  id: number
@@ts_fields@@}
export interface Create@@name@@Input {
@@ts_inputs@@}
export type Update@@name@@Input = Partial<Create@@name@@Input>
export interface @@name@@Query extends ListQuery {
@@ts_search@@}
export const columns = (t: (key: string) => string): Column[] =>
  (@@columns@@ as Column[]).map((column) => ({ ...column, label: t(column.label) }))
export const search: string[] = @@search@@
export const defaults = @@defaults@@

export default createResourceApi<@@name@@, Create@@name@@Input, Update@@name@@Input, @@name@@Query>('/@@resource@@')
