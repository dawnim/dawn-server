Register app.@@table@@ list/create/update/delete permissions and the menu in app/admin/menu.go. Call controllers.Register@@name@@ in app/admin/routes.go.

Register the frontend page in createAdmin pages:

```ts
'/business/@@table@@': () => import('./views/business/@@table@@/index.vue')
```

API and field definitions: admin/src/api/business/@@table@@.ts. Reference mode uses the built-in CrudPage; source mode includes editable list, modules/table-search.vue and modules/edit-dialog.vue files.

Review field types, validation and Scope/Prepare. The business table uses an id primary key and integer created_at, updated_at and deleted_at timestamps.

Field mappings are generated under admin/src/locales/zh/@@table@@.ts and en/@@table@@.ts and loaded by the page. Technical field names remain unchanged. Shared messages use Chinese source keys; missing translations still interpolate key placeholders.

Select Generate migration to snapshot the current schema under database/migrations. Existing tables are left unchanged. Add incremental migrations for later field changes, then run migrations. Review business constraints before deployment.
