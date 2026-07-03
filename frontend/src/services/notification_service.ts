import { reactive } from 'vue'

export interface Notification {
  id: number
  message: string
  type: 'success' | 'error' | 'info'
  duration?: number
}

const state = reactive({
  notifications: [] as Notification[]
})

let nextId = 1

// Track auto-dismiss timeout IDs by notification id so they can be cleared when
// a notification is removed (manual or auto), preventing stale callbacks from
// mutating the array after teardown.
const dismissTimers = new Map<number, ReturnType<typeof setTimeout>>()

const clearTimer = (id: number) => {
  const timer = dismissTimers.get(id)
  if (timer !== undefined) {
    clearTimeout(timer)
    dismissTimers.delete(id)
  }
}

export const useNotifications = () => {
  const notify = (message: string, type: 'success' | 'error' | 'info' = 'info', duration = 5000) => {
    const id = nextId++
    const notification: Notification = { id, message, type, duration }
    state.notifications.push(notification)

    if (duration > 0) {
      const timer = setTimeout(() => {
        remove(id)
      }, duration)
      dismissTimers.set(id, timer)
    }
  }

  const remove = (id: number) => {
    clearTimer(id)
    const index = state.notifications.findIndex(n => n.id === id)
    if (index !== -1) {
      state.notifications.splice(index, 1)
    }
  }

  return {
    notifications: state.notifications,
    notify,
    remove
  }
}
