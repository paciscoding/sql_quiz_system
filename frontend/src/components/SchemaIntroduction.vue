<script setup lang="ts">
import EmptyState from './EmptyState.vue'
import type { TableIntrospection } from '../services/practice'

withDefaults(defineProps<{
  introduction: string
  tables: TableIntrospection[]
  imported?: boolean
  title?: string
  mode?: 'student' | 'lecturer-preview'
}>(), {
  imported: true,
  title: undefined,
  mode: 'lecturer-preview',
})
</script>

<template>
  <div class="schema-intro">
    <h3 v-if="title" class="schema-intro__title">{{ title }}</h3>

    <!-- Description text — rendered as plain text, never v-html -->
    <div v-if="introduction" class="schema-intro__description">
      <p class="schema-intro__description-text">{{ introduction }}</p>
    </div>

    <!-- Empty state and table list — hidden from students (they get the image window instead) -->
    <template v-if="mode !== 'student'">
    <!-- Empty state when no tables have been imported yet -->
    <EmptyState
      v-if="!imported || tables.length === 0"
      title="No database imported yet"
      body="No database has been imported for this category/curriculum yet."
    />

    <!-- Table list -->
    <template v-else>
      <div
        v-for="table in tables"
        :key="table.name"
        class="schema-intro__table-block"
      >
        <h4 class="schema-intro__table-name">{{ table.name }}</h4>
        <table class="schema-intro__col-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>Nullable</th>
              <th>Keys</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="col in table.columns" :key="col.name">
              <td class="col-name">{{ col.name }}</td>
              <td class="col-type">{{ col.data_type }}</td>
              <td class="col-nullable">{{ col.nullable ? 'Yes' : 'No' }}</td>
              <td class="col-keys">
                <span v-if="col.is_primary_key" class="key-badge pk-badge">PK</span>
                <span
                  v-if="col.is_foreign_key"
                  class="key-badge fk-badge"
                  :title="'References ' + col.references_table + '.' + col.references_column"
                >FK &rarr; {{ col.references_table }}.{{ col.references_column }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    </template><!-- /mode !== 'student' -->
  </div>
</template>

<style scoped>
.schema-intro {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}

.schema-intro__title {
  margin: 0;
  font-size: 1.05rem;
  font-weight: 700;
  color: var(--color-text);
}

.schema-intro__description {
  padding: 12px 14px;
  background: var(--alert-info-bg);
  border: 1px solid var(--alert-info-border);
  border-radius: var(--radius);
  color: var(--alert-info-text);
}

.schema-intro__description-text {
  margin: 0;
  white-space: pre-wrap;
  font-size: 0.95rem;
  line-height: 1.55;
}

.schema-intro__table-block {
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--color-surface);
  box-shadow: var(--shadow-1);
}

.schema-intro__table-name {
  margin: 0;
  padding: 8px 14px;
  background: var(--color-surface-sunk);
  font-size: 0.88rem;
  font-weight: 700;
  color: var(--color-text);
  border-bottom: 1px solid var(--color-border);
  font-family: var(--font-mono);
  letter-spacing: 0.01em;
}

.schema-intro__col-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

.schema-intro__col-table th,
.schema-intro__col-table td {
  padding: 6px 12px;
  border-bottom: 1px solid var(--color-border-soft);
  text-align: left;
  vertical-align: middle;
}
.schema-intro__col-table tr:last-child td { border-bottom: none; }
.schema-intro__col-table tr:nth-child(even) td { background: var(--color-surface-muted); }

.schema-intro__col-table th {
  background: var(--color-surface-muted);
  font-weight: 700;
  font-size: 0.7rem;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  border-bottom: 1px solid var(--color-border);
}

.col-name {
  font-family: var(--font-mono);
  font-weight: 600;
  color: var(--color-text);
}
.col-type {
  font-family: var(--font-mono);
  color: var(--color-text-muted);
  font-size: 0.82rem;
}
.col-nullable {
  color: var(--color-text-muted);
  font-size: 0.82rem;
}
.col-keys {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.key-badge {
  padding: 1px 7px;
  border: 1px solid var(--color-border-soft);
  border-radius: 999px;
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  white-space: nowrap;
}
.pk-badge {
  background: var(--color-primary);
  color: #fff;
  border-color: var(--color-primary-strong);
}
.fk-badge {
  background: var(--color-primary-bg);
  color: var(--color-primary-strong);
  border-color: var(--color-primary);
  font-family: var(--font-mono);
  font-size: 0.7rem;
  padding: 1px 7px;
}
</style>
