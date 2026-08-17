import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import EmailJobBubble from '../../components/EmailJobBubble.vue'

const runningJob = { job_id: 'j1', total: 40, sent: 12, failed: 2, done: false, failures: ['s7', 's9'] }

describe('EmailJobBubble', () => {
  it('renders nothing when idle', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: null, state: 'idle' } })
    expect(wrapper.find('[data-testid="email-job-bubble"]').exists()).toBe(false)
  })

  it('shows the sent/total counter and the failure count while running', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: runningJob, state: 'running' } })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('12/40')
    expect(text).toContain('2 failed')
  })

  it('lists the failed login IDs when the job finishes with failures', () => {
    const wrapper = mount(EmailJobBubble, {
      props: { job: { ...runningJob, sent: 38, done: true }, state: 'done' },
    })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('s7')
    expect(text).toContain('s9')
  })

  it('reports a clean finish without a failure list', () => {
    const wrapper = mount(EmailJobBubble, {
      props: { job: { job_id: 'j1', total: 3, sent: 3, failed: 0, done: true, failures: [] }, state: 'done' },
    })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('3/3')
    expect(wrapper.find('[data-testid="email-job-failures"]').exists()).toBe(false)
  })

  it('explains the unavailable state instead of showing an error', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: null, state: 'unavailable' } })
    expect(wrapper.find('[data-testid="email-job-bubble"]').text().toLowerCase())
      .toContain('progress unavailable')
  })

  it('emits dismiss when closed', async () => {
    const wrapper = mount(EmailJobBubble, { props: { job: runningJob, state: 'running' } })
    await wrapper.find('[data-testid="email-job-dismiss"]').trigger('click')
    expect(wrapper.emitted('dismiss')).toHaveLength(1)
  })

  it('fully lights the dot track when done with partial failures, not stuck mid-progress', () => {
    const wrapper = mount(EmailJobBubble, {
      props: {
        job: { job_id: 'j1', total: 40, sent: 36, failed: 4, done: true, failures: ['s7', 's9'] },
        state: 'done',
      },
    })
    const dots = wrapper.findAll('[data-testid="email-job-bubble"] .dot.filled')
    expect(dots).toHaveLength(10)
  })
})
