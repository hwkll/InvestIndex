<script setup>
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useApp } from '../store';

const app = useApp();
const router = useRouter();
const route = useRoute();

const pin = ref('');
const busy = ref(false);
const err = ref('');

async function submit() {
  if (busy.value) return;
  err.value = '';
  busy.value = true;
  try {
    await app.login(pin.value);
    router.replace(route.query.r || '/');
  } catch (e) {
    err.value = e.message || '登录失败';
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <h1>◈ InvestHub</h1>
      <div class="sub">个人综合投资管理平台 · 本地自托管</div>
      <form @submit.prevent="submit">
        <div class="field">
          <label>访问口令</label>
          <input v-model="pin" type="password" autocomplete="current-password" placeholder="请输入访问口令" autofocus />
          <div v-if="err" class="hint" style="color: var(--up)">{{ err }}</div>
        </div>
        <button class="btn" style="width: 100%" :disabled="busy">
          <span v-if="busy" class="spin"></span>{{ busy ? ' 验证中' : '进入' }}
        </button>
      </form>
      <div class="hint mt12" style="text-align: center">数据仅保存在本机 data/investhub.db</div>
    </div>
  </div>
</template>
