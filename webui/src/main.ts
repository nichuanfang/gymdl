import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./views/Dashboard.vue') },
    { path: '/download', component: () => import('./views/Download.vue') },
    { path: '/history', component: () => import('./views/History.vue') },
    { path: '/files', component: () => import('./views/Files.vue') },
    { path: '/settings', component: () => import('./views/Settings.vue') },
    { path: '/logs', component: () => import('./views/Logs.vue') },
    { path: '/cron', component: () => import('./views/Cron.vue') },
    { path: '/qqlogin', component: () => import('./views/QQLogin.vue') },
    { path: '/search', component: () => import('./views/Search.vue') },
    { path: '/listening', component: () => import('./views/Listening.vue') },
    { path: '/rediscover', component: () => import('./views/Rediscover.vue') },
    { path: '/favorites', component: () => import('./views/Favorites.vue') },
    { path: '/playlist-desc', component: () => import('./views/PlaylistDesc.vue') },
    { path: '/ai-playlist', component: () => import('./views/AIPlaylist.vue') },
    { path: '/duplicates', component: () => import('./views/Duplicates.vue') },
  ],
})

createApp(App).use(router).mount('#app')
