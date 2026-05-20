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

export const useNotifications = () => {
  const notify = (message: string, type: 'success' | 'error' | 'info' = 'info', duration = 5000) => {
    const id = nextId++
    const notification: Notification = { id, message, type, duration }
    state.notifications.push(notification)

    if (duration > 0) {
      setTimeout(() => {
        remove(id)
      }, duration)
    }
  }

  const remove = (id: number) => {
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
