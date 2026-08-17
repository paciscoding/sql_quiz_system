<script setup lang="ts">
import { computed } from 'vue'
import type { EmailJobStatus, EmailJobState } from '../composables/useEmailJobStatus'

const props = defineProps<{
  job: EmailJobStatus | null
  state: EmailJobState
}>()

defineEmits<{ (e: 'dismiss'): void }>()

const visible = computed(() => props.state !== 'idle')
// The counter tracks successful sends against the roster total; failures are
// surfaced separately (a "12/40, 2 failed" split reads clearer than folding
// failures into the numerator).
const sentCount = computed(() => props.job?.sent ?? 0)
const total = computed(() => props.job?.total ?? 0)
const failed = computed(() => props.job?.failed ?? 0)
const failures = computed(() => props.job?.failures ?? [])

const tone = computed(() => {
  if (props.state === 'unavailable') return 'neutral'
  if (props.state === 'done') return failed.value > 0 ? 'warn' : 'ok'
  return 'busy'
})

// Ten dots is a fixed-width progress track — it reads at a glance without the
// layout shifting as the roster size changes. The track fills on ATTEMPTED
// sends (sent + failed), not successes: once the worker is done, attempted
// always equals total, so the track reads as finished even when some sends
// failed. The numeral above stays sent-only — it answers "how many students
// actually received their password."
const filledDots = computed(() => {
  if (!total.value) return 0
  return Math.round(((sentCount.value + failed.value) / total.value) * 10)
})
</script>

<template>
  <div v-if="visible" class="bubble" :class="tone" data-testid="email-job-bubble" role="status">
    <button
      type="button"
      class="dismiss"
      data-testid="email-job-dismiss"
      aria-label="Dismiss email progress"
      @click="$emit('dismiss')"
    >&times;</button>

    <template v-if="state === 'unavailable'">
      <div class="headline">&#9993; Progress unavailable</div>
      <div class="detail">The send is still running or has finished; every password is already in the results table.</div>
    </template>

    <template v-else>
      <div class="headline">
        &#9993; {{ state === 'done' ? 'Passwords emailed' : 'Emailing passwords' }}
        <span class="count">{{ sentCount }}/{{ total }}</span>
      </div>
      <div class="track" aria-hidden="true">
        <span v-for="n in 10" :key="n" class="dot" :class="{ filled: n <= filledDots }">&#9679;</span>
      </div>
      <div v-if="failed > 0" class="detail">{{ failed }} failed</div>
      <div v-if="state === 'done' && failures.length" class="detail" data-testid="email-job-failures">
        Not delivered: {{ failures.join(', ') }} — copy their passwords from the table.
      </div>
    </template>
  </div>
</template>

<style scoped>
.bubble {
  position: fixed;
  top: calc(var(--space-6) + 3rem);
  right: var(--space-6);
  z-index: 900;
  max-width: 22rem;
  padding: var(--space-4);
  padding-right: var(--space-6);
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-size: 0.85rem;
  color: var(--color-text);
}
.bubble.ok { border-color: var(--color-success); }
.bubble.warn { border-color: var(--color-warning); }
.headline {
  font-weight: 800;
  display: flex;
  gap: var(--space-2);
  align-items: baseline;
  flex-wrap: wrap;
}
.count { font-family: var(--font-mono); }
.detail { margin-top: var(--space-1); color: var(--color-text-muted); }
.track { margin-top: var(--space-1); letter-spacing: 0.15em; color: var(--color-border); }
.dot.filled { color: var(--color-text); }
.bubble.busy .dot.filled { animation: pulse 1.4s ease-in-out infinite; }
.dismiss {
  position: absolute;
  top: 2px;
  right: 6px;
  border: none;
  background: none;
  color: var(--color-text-muted);
  font-size: 1.1rem;
  line-height: 1;
  cursor: pointer;
}
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}
@media (prefers-reduced-motion: reduce) {
  .bubble.busy .dot.filled { animation: none; }
}
</style>
