import { createRouter, createWebHistory } from 'vue-router';

const routes = [
  { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
  { path: '/', name: 'dashboard', component: () => import('./views/DashboardView.vue') },
  { path: '/positions', name: 'positions', component: () => import('./views/PositionsView.vue') },
  { path: '/assets/:id', name: 'asset', component: () => import('./views/AssetDetailView.vue'), props: true },
  { path: '/transactions', name: 'transactions', component: () => import('./views/TransactionsView.vue') },
  { path: '/ai', name: 'ai', component: () => import('./views/AiView.vue') },
  { path: '/alerts', name: 'alerts', component: () => import('./views/AlertsView.vue') },
  { path: '/cash', name: 'cash', component: () => import('./views/CashView.vue') },
  { path: '/watchlist', name: 'watchlist', component: () => import('./views/WatchlistView.vue') },
  { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
});
