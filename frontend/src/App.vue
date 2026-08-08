<script setup>
import { computed, onMounted, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useApp } from './store';
import ToastHost from './components/ToastHost.vue';

const app = useApp();
const route = useRoute();
const router = useRouter();

const NAV = [
  { to: '/', ico: '◧', label: '仪表盘' },
  { to: '/positions', ico: '◨', label: '持仓' },
  { to: '/transactions', ico: '⇄', label: '交易流水' },
  { to: '/ai', ico: '✦', label: 'AI 分析' },
  { to: '/alerts', ico: '◑', label: '提醒', badge: true },
  { to: '/cash', ico: '¤', label: '现金账户' },
  { to: '/watchlist', ico: '★', label: '自选' },
  { to: '/settings', ico: '⚙', label: '设置' },
];

const isLogin = computed(() => route.name === 'login');

async function logout() {
  await app.logout();
  router.push('/login');
}

onMounted(() => {
  if (app.loggedIn || !app.authRequired) app.refreshUnread();
});

watch(() => app.loggedIn, (v) => { if (v) app.refreshUnread(); });
</script>

<template>
  <ToastHost />
  <router-view v-if="isLogin" />
  <div v-else class="app">
    <aside class="sidebar">
      <div class="brand">
        <span class="logo">◈</span>
        <span class="brand-name">InvestHub</span>
      </div>
      <nav class="nav">
        <router-link v-for="n in NAV" :key="n.to" :to="n.to" class="nav-item">
          <span class="nav-ico">{{ n.ico }}</span>
          <span class="nl">{{ n.label }}</span>
          <span v-if="n.badge && app.unread > 0" class="nav-badge">{{ app.unread }}</span>
        </router-link>
      </nav>
      <div class="side-foot">
        <button v-if="app.authRequired" class="btn-ghost" @click="logout">退出登录</button>
        <div class="ver">
          v{{ app.version || '1.1.0' }} · 本地自托管
          <span :title="app.sseOpen ? '实时推送已连接' : '实时推送断开'">{{ app.sseOpen ? '●' : '○' }}</span>
        </div>
      </div>
    </aside>
    <main class="main">
      <router-view v-slot="{ Component, route: r }">
        <Transition :name="'page'" mode="out-in">
          <component :is="Component" :key="r.path" />
        </Transition>
      </router-view>
    </main>
  </div>

  <!-- Global confirm dialog -->
  <Teleport to="body">
    <div v-if="app.confirmDialog" class="modal-mask" @click.self="app.closeConfirm(false)">
      <div class="modal" style="width: 400px">
        <h3>{{ app.confirmDialog.title || '确认操作' }}</h3>
        <p>{{ app.confirmDialog.message }}</p>
        <p v-if="app.confirmDialog.detail" class="muted" style="font-size:13px">{{ app.confirmDialog.detail }}</p>
        <div class="modal-foot">
          <button class="btn ghost2" @click="app.closeConfirm(false)">取消</button>
          <button class="btn" :class="{ danger: app.confirmDialog.danger }" @click="app.closeConfirm(true)">{{ app.confirmDialog.okText || '确认' }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
