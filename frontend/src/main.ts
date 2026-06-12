import { createApp } from 'vue'
import './style.css'
import App from './App.vue'
import router from './router'
import { useAuth } from './services/auth'

// Seed auth state from localStorage before mounting so router guards
// and components see isLoggedIn as true synchronously on hard refresh.
useAuth().hydrateFromStorage()

const app = createApp(App)
app.use(router)
app.mount('#app')
