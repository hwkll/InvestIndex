import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import { router } from './router';
import { useApp } from './store';
import './echarts';
import './styles.css';

const app = createApp(App);
app.use(createPinia());
app.use(router);

const store = useApp();

// Guard routes once the auth status is known.
router.beforeEach(async (to) => {
  if (!store.ready) await store.bootstrap();
  const needsAuth = store.authRequired && !store.loggedIn;
  if (needsAuth && !to.meta.public) return { name: 'login', query: { r: to.fullPath } };
  if (!needsAuth && to.name === 'login') return { path: '/' };
  return true;
});

app.mount('#app');
