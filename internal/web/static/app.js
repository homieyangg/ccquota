'use strict';

// ── i18n helpers (shared, used inside Alpine component) ───────────────────────

const SUPPORTED_LANGS = ['en', 'zh-TW', 'zh-CN'];
const i18nCache = {};

function detectLang() {
  const q = new URLSearchParams(location.search).get('lang');
  if (q && SUPPORTED_LANGS.includes(q)) return q;
  const ls = localStorage.getItem('ccquota_lang');
  if (ls && SUPPORTED_LANGS.includes(ls)) return ls;
  const nav = navigator.language || '';
  if (nav.startsWith('zh-TW') || nav.startsWith('zh-Hant')) return 'zh-TW';
  if (nav.startsWith('zh')) return 'zh-CN';
  return 'en';
}

async function loadLang(lang) {
  if (i18nCache[lang]) return i18nCache[lang];
  const res = await fetch(`/i18n/${lang}.json`);
  if (!res.ok) throw new Error(`i18n load failed: ${res.status}`);
  const data = await res.json();
  i18nCache[lang] = data;
  return data;
}

// ── API helpers ───────────────────────────────────────────────────────────────

async function apiGet(path) {
  const res = await fetch(path, { credentials: 'include' });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

async function apiPost(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error || `${res.status}`);
  }
  return res.json();
}

// ── Sparkline (canvas) ────────────────────────────────────────────────────────

function drawSparkline(canvas, points) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || 300;
  const h = 40;
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);

  if (!points || points.length < 2) {
    ctx.fillStyle = '#30363d';
    ctx.font = '10px sans-serif';
    ctx.fillText('—', w / 2 - 4, h / 2 + 3);
    return;
  }

  const values = points.map(p => p.seven_day);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;

  const pad = 4;
  const yw = h - pad * 2;
  const xw = w - pad * 2;

  ctx.beginPath();
  points.forEach((p, i) => {
    const x = pad + (i / (points.length - 1)) * xw;
    const y = pad + yw - ((p.seven_day - min) / range) * yw;
    i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
  });
  ctx.strokeStyle = '#58a6ff';
  ctx.lineWidth = 1.5;
  ctx.stroke();
}

// ── Alpine component ──────────────────────────────────────────────────────────

document.addEventListener('alpine:init', () => {
  Alpine.data('dashboard', () => ({
    // i18n
    lang: 'en',
    translations: {},

    // accounts
    accounts: [],
    lastUpdated: '',

    // enroll form
    enrollAccount: '',
    enrollUser: '',
    enrollOneliner: '',
    enrollMsg: '',
    enrollMsgType: '', // 'success' | 'error'
    enrollBtnDisabled: false,

    // connect form
    connectLabel: '',
    connectId: '',
    oauthCode: '',
    authorizeUrl: '',
    loginId: null,
    showOauthFlow: false,
    connectMsg: '',
    connectMsgType: '', // 'success' | 'error'
    startBtnDisabled: false,

    // per-account sparkline history keyed by id
    historyMap: {},

    // countdown timer handle
    _countdownHandle: null,

    // ── helpers ──────────────────────────────────────────────────────────────

    t(key) {
      return this.translations[key] || (i18nCache['en'] && i18nCache['en'][key]) || key;
    },

    async setLang(lang) {
      this.lang = lang;
      localStorage.setItem('ccquota_lang', lang);
      try {
        if (!i18nCache['en']) await loadLang('en');
        this.translations = await loadLang(lang);
      } catch (e) {
        console.error('Failed to load language', lang, e);
        this.translations = i18nCache['en'] || {};
      }
    },

    fmtPct(v) {
      return v == null ? '—' : v.toFixed(1) + '%';
    },

    fillClass(pct) {
      if (pct >= 90) return 'fill-red';
      if (pct >= 70) return 'fill-yellow';
      return 'fill-green';
    },

    countdown(resetsAt) {
      if (!resetsAt) return '';
      const diff = resetsAt - Math.floor(Date.now() / 1000);
      if (diff <= 0) return '—';
      const d = Math.floor(diff / 86400);
      const h = Math.floor((diff % 86400) / 3600);
      const m = Math.floor((diff % 3600) / 60);
      const s = diff % 60;
      const parts = [];
      if (d) parts.push(d + this.t('days'));
      if (h) parts.push(h + this.t('hours'));
      if (m) parts.push(m + this.t('minutes'));
      if (!d && !h) parts.push(s + this.t('seconds'));
      return parts.join(' ');
    },

    clampPct(v) {
      return Math.min(v || 0, 100);
    },

    // ── API ───────────────────────────────────────────────────────────────────

    async refreshAccounts() {
      try {
        const data = await apiGet('/api/accounts');
        this.accounts = data;
        this.lastUpdated = this.t('last_updated') + ' ' + new Date().toLocaleTimeString();
        // 預設選第一個帳號（若尚未選取）
        if (data.length > 0 && !this.enrollAccount) {
          this.enrollAccount = data[0].id;
        }
        // fetch sparklines after accounts update
        await this._loadSparklines(data);
      } catch (e) {
        console.error('refresh failed', e);
      }
    },

    async _loadSparklines(accounts) {
      const tasks = accounts
        .filter(a => a.has_reading)
        .map(async a => {
          try {
            const pts = await apiGet(`/api/history?account=${encodeURIComponent(a.id)}&hours=168`);
            this.historyMap = { ...this.historyMap, [a.id]: pts };
            // draw after next tick so canvas elements exist in DOM
            this.$nextTick(() => this._drawSparkline(a.id));
          } catch (e) {
            // sparkline non-critical
          }
        });
      await Promise.all(tasks);
    },

    _drawSparkline(id) {
      const canvas = document.getElementById('spark-' + id);
      if (!canvas) return;
      drawSparkline(canvas, this.historyMap[id] || []);
    },

    // redraw sparklines after accounts re-render
    async afterAccountsRender() {
      await this.$nextTick();
      for (const a of this.accounts) {
        if (a.has_reading) this._drawSparkline(a.id);
      }
    },

    // ── Enrollment flow ───────────────────────────────────────────────────────

    async generateEnrollLink() {
      this.enrollBtnDisabled = true;
      this.enrollMsg = '';
      this.enrollMsgType = '';
      this.enrollOneliner = '';
      try {
        const data = await apiPost('/api/enroll', {
          account: this.enrollAccount,
          user: this.enrollUser,
        });
        this.enrollOneliner = `bash <(curl -fsSL ${data.url})`;
      } catch (e) {
        this.enrollMsg = this.t('enroll_error') + ' ' + e.message;
        this.enrollMsgType = 'error';
      } finally {
        this.enrollBtnDisabled = false;
      }
    },

    copyEnrollLink() {
      if (this.enrollOneliner) {
        navigator.clipboard.writeText(this.enrollOneliner).catch(() => {});
      }
    },

    // ── Login flow ────────────────────────────────────────────────────────────

    async startLogin() {
      this.startBtnDisabled = true;
      this.connectMsg = '';
      this.connectMsgType = '';
      try {
        const data = await apiPost('/api/login/start', { label: this.connectLabel });
        this.loginId = data.login_id;
        this.authorizeUrl = data.authorize_url;
        this.showOauthFlow = true;
      } catch (e) {
        this.connectMsg = this.t('connect_error') + ' ' + e.message;
        this.connectMsgType = 'error';
      } finally {
        this.startBtnDisabled = false;
      }
    },

    openUrl() {
      if (this.authorizeUrl) window.open(this.authorizeUrl, '_blank');
    },

    async completeLogin() {
      if (!this.connectId) {
        this.connectMsg = this.t('connect_error') + ' Account ID is required.';
        this.connectMsgType = 'error';
        return;
      }
      if (!this.oauthCode) {
        this.connectMsg = this.t('connect_error') + ' Code is required.';
        this.connectMsgType = 'error';
        return;
      }
      try {
        await apiPost('/api/login/complete', {
          login_id: this.loginId,
          id: this.connectId,
          label: this.connectLabel,
          code: this.oauthCode,
        });
        this.connectMsg = this.t('connect_success');
        this.connectMsgType = 'success';
        this.showOauthFlow = false;
        this.oauthCode = '';
        this.loginId = null;
        await this.refreshAccounts();
      } catch (e) {
        this.connectMsg = this.t('connect_error') + ' ' + e.message;
        this.connectMsgType = 'error';
      }
    },

    // ── Lifecycle ─────────────────────────────────────────────────────────────

    async init() {
      // load i18n
      this.lang = detectLang();
      try {
        if (!i18nCache['en']) await loadLang('en');
        this.translations = await loadLang(this.lang);
      } catch (e) {
        this.translations = i18nCache['en'] || {};
      }

      // initial load
      await this.refreshAccounts();

      // poll every 30s
      setInterval(() => this.refreshAccounts(), 30000);

      // countdown tick every second
      this._countdownHandle = setInterval(() => {
        // trigger reactivity by reassigning accounts (shallow copy triggers Alpine watchers)
        // We only need the countdown text to update, so a no-op reassign works.
        this.accounts = this.accounts.slice();
      }, 1000);
    },
  }));
});
