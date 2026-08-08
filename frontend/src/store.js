import { defineStore } from 'pinia';
import { Api } from './api';

let toastSeq = 0;

// Global app state: session, live quotes pushed over SSE, toasts, alert badge.
export const useApp = defineStore('app', {
  state: () => ({
    ready: false,
    authRequired: false,
    loggedIn: false,
    version: '',
    quotes: {},        // assetId -> {price, chgPct, status, ...}
    toasts: [],
    unread: 0,
    events: [],
    sseOpen: false,
    _es: null,
    _quoteTick: 0,     // bumped on every quote frame so views can react
    _confirmResolve: null,
    confirmDialog: null, // { title, message, detail, danger }
  }),
  actions: {
    async bootstrap() {
      try {
        const s = await Api.authStatus();
        this.authRequired = s.authRequired;
        this.loggedIn = s.loggedIn;
        this.version = s.version;
      } catch (e) {
        this.toast('无法连接后端', e.message, 'error');
      }
      this.ready = true;
      if (!this.authRequired || this.loggedIn) this.connect();
    },

    async login(pin) {
      await Api.login(pin);
      this.loggedIn = true;
      this.connect();
    },

    async logout() {
      try { await Api.logout(); } catch { /* ignore */ }
      this.loggedIn = false;
      this.disconnect();
    },

    connect() {
      if (this._es) return;
      this._connectSSE();
    },

    _connectSSE(retryDelay = 1000) {
      const es = new EventSource('/api/v1/events', { withCredentials: true });
      this._es = es;
      this._retryTimer = null;

      es.onopen = () => {
        this.sseOpen = true;
        // Reset retry delay on successful connection.
        this._retryDelay = 1000;
      };

      es.onerror = () => {
        this.sseOpen = false;
        es.close();
        this._es = null;
        // Exponential backoff: 1s → 2s → 4s → ... → max 30s
        const delay = Math.min(this._retryDelay || 1000, 30000);
        this._retryTimer = setTimeout(() => {
          if (this.loggedIn && !this._es) this._connectSSE(delay * 2);
        }, delay);
        this._retryDelay = delay * 2;
      };
      es.addEventListener('quote', (ev) => {
        try {
          const q = JSON.parse(ev.data);
          this.quotes[q.assetId] = q;
          this._quoteTick++;
        } catch { /* ignore */ }
      });
      es.addEventListener('alert', (ev) => {
        try {
          const a = JSON.parse(ev.data);
          this.unread++;
          this.events.unshift(a);
          // Keep events array bounded to prevent memory growth.
          if (this.events.length > 200) this.events.pop();
          this.toast('提醒触发', a.message, 'alert');
        } catch { /* ignore */ }
      });
      es.addEventListener('ai_done', (ev) => {
        try {
          const a = JSON.parse(ev.data);
          this.toast('AI 分析完成', `信号：${a.signal}（${a.model}）`, 'success');
        } catch { /* ignore */ }
      });
    },

    disconnect() {
      if (this._retryTimer) { clearTimeout(this._retryTimer); this._retryTimer = null; }
      if (this._es) { this._es.close(); this._es = null; }
      this.sseOpen = false;
    },

    toast(title, body = '', kind = '') {
      const id = ++toastSeq;
      this.toasts.push({ id, title, body, kind });
      setTimeout(() => this.dismiss(id), kind === 'error' ? 6000 : 4200);
    },

    dismiss(id) {
      const i = this.toasts.findIndex((t) => t.id === id);
      if (i >= 0) this.toasts.splice(i, 1);
    },

    // Returns a Promise that resolves true/false — use like: if (await app.confirm({...}))
    confirm(opts) {
      return new Promise((resolve) => {
        this._confirmResolve = resolve;
        this.confirmDialog = opts;
      });
    },

    closeConfirm(ok) {
      if (this._confirmResolve) { this._confirmResolve(ok); this._confirmResolve = null; }
      this.confirmDialog = null;
    },

    async refreshUnread() {
      try {
        const r = await Api.alertEvents();
        this.unread = r.unread || 0;
        this.events = r.items || [];
      } catch { /* ignore */ }
    },

    // Merge the live SSE price into a position/asset row.
    live(row) {
      const q = this.quotes[row.assetId || row.id];
      if (!q) return row;
      const price = q.price;
      const qty = row.qty ?? 0;
      const marketValue = qty > 0 ? qty * price : row.marketValue;
      const floatingPnl = qty > 0 ? marketValue - (row.costTotal ?? 0) : row.floatingPnl;
      return {
        ...row,
        price,
        chgPct: q.chgPct,
        quoteStatus: q.status,
        marketValue,
        floatingPnl,
        floatingPct: row.costTotal > 0 ? floatingPnl / row.costTotal : row.floatingPct,
      };
    },
  },
});
