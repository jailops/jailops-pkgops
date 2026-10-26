import { createApp } from 'vue'
import App from './App.vue'
import { applyTheme } from './theme'
import './styles.css'

applyTheme()
createApp(App).mount('#app')
