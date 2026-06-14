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
    ctx.fillText('N/A', w / 2 - 4, h / 2 + 3);
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
    langOpen: false,

    // 認證狀態
    authReady: false, // checkAuth 完成前不顯示任何畫面，避免登入頁閃一下
    authed: false,
    mustChange: false,
    _lastLoginPassword: '',
    loginPassword: '',
    loginError: '',
    loginBtnDisabled: false,

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

    // settings modal
    settingsCurPw: '',
    settingsNewPw: '',
    settingsConfirmPw: '',
    settingsMsg: '',
    settingsMsgType: '',
    settingsBtnDisabled: false,

    // must-change modal
    mustChangeCurPw: '',
    mustChangeNewPw: '',
    mustChangeConfirmPw: '',
    mustChangeMsg: '',
    mustChangeBtnDisabled: false,

    // ── helpers ──────────────────────────────────────────────────────────────

    t(key) {
      return this.translations[key] || (i18nCache['en'] && i18nCache['en'][key]) || key;
    },

    async setLang(lang) {
      this.lang = lang;
      this.langOpen = false;
      localStorage.setItem('ccquota_lang', lang);
      try {
        if (!i18nCache['en']) await loadLang('en');
        this.translations = await loadLang(lang);
      } catch (e) {
        console.error('Failed to load language', lang, e);
        this.translations = i18nCache['en'] || {};
      }
    },

    langShortLabel(lang) {
      const map = { 'en': 'EN', 'zh-TW': '繁中', 'zh-CN': '简中' };
      return map[lang] || lang;
    },

    fmtPct(v) {
      return v == null ? 'N/A' : v.toFixed(1) + '%';
    },

    fillClass(pct) {
      if (pct >= 90) return 'fill-red';
      if (pct >= 70) return 'fill-yellow';
      return 'fill-green';
    },

    countdown(resetsAt) {
      if (!resetsAt) return '';
      const diff = resetsAt - Math.floor(Date.now() / 1000);
      if (diff <= 0) return 'N/A';
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

    // ── 認證 ──────────────────────────────────────────────────────────────────

    async checkAuth() {
      try {
        const data = await apiGet('/api/auth/status');
        this.authed = !!data.authed;
        this.mustChange = !!data.must_change;
      } catch (e) {
        this.authed = false;
        this.mustChange = false;
      }
    },

    async doLogin() {
      this.loginBtnDisabled = true;
      this.loginError = '';
      try {
        await apiPost('/api/auth/login', { password: this.loginPassword });
        this._lastLoginPassword = this.loginPassword;
        this.loginPassword = '';
        const status = await apiGet('/api/auth/status');
        this.authed = !!status.authed;
        this.mustChange = !!status.must_change;
        if (this.mustChange) {
          this.$nextTick(() => {
            this.mustChangeCurPw = this._lastLoginPassword;
            this.$refs.mustChangeDialog && this.$refs.mustChangeDialog.showModal();
          });
        } else {
          await this.refreshAccounts();
        }
      } catch (e) {
        this.loginError = this.t('login_error');
      } finally {
        this.loginBtnDisabled = false;
      }
    },

    async doLogout() {
      try {
        await apiPost('/api/auth/logout', {});
      } catch (e) {
        // 忽略錯誤，強制回登入畫面
      }
      this.authed = false;
      this.mustChange = false;
      this._lastLoginPassword = '';
      this.accounts = [];
      this.lastUpdated = '';
    },

    // ── Settings modal ────────────────────────────────────────────────────────

    openSettingsModal() {
      this.settingsCurPw = '';
      this.settingsNewPw = '';
      this.settingsConfirmPw = '';
      this.settingsMsg = '';
      this.settingsMsgType = '';
      this.settingsBtnDisabled = false;
      this.$refs.settingsDialog && this.$refs.settingsDialog.showModal();
    },

    closeSettingsModal() {
      this.$refs.settingsDialog && this.$refs.settingsDialog.close();
    },

    async doChangePassword() {
      this.settingsMsg = '';
      this.settingsMsgType = '';
      if (this.settingsNewPw !== this.settingsConfirmPw) {
        this.settingsMsg = this.t('password_mismatch');
        this.settingsMsgType = 'error';
        return;
      }
      if (this.settingsNewPw.length < 8) {
        this.settingsMsg = this.t('password_too_short');
        this.settingsMsgType = 'error';
        return;
      }
      this.settingsBtnDisabled = true;
      try {
        await apiPost('/api/auth/change-password', {
          current: this.settingsCurPw,
          new: this.settingsNewPw,
        });
        this.settingsMsg = this.t('password_changed');
        this.settingsMsgType = 'success';
        this.settingsCurPw = '';
        this.settingsNewPw = '';
        this.settingsConfirmPw = '';
      } catch (e) {
        this.settingsMsg = e.message;
        this.settingsMsgType = 'error';
      } finally {
        this.settingsBtnDisabled = false;
      }
    },

    // ── Must-change modal (non-dismissible) ───────────────────────────────────

    async doMustChangePassword() {
      this.mustChangeMsg = '';
      if (this.mustChangeNewPw !== this.mustChangeConfirmPw) {
        this.mustChangeMsg = this.t('password_mismatch');
        return;
      }
      if (this.mustChangeNewPw.length < 8) {
        this.mustChangeMsg = this.t('password_too_short');
        return;
      }
      this.mustChangeBtnDisabled = true;
      try {
        await apiPost('/api/auth/change-password', {
          current: this.mustChangeCurPw,
          new: this.mustChangeNewPw,
        });
        this.mustChange = false;
        this._lastLoginPassword = '';
        this.$refs.mustChangeDialog && this.$refs.mustChangeDialog.close();
        await this.refreshAccounts();
      } catch (e) {
        this.mustChangeMsg = e.message;
      } finally {
        this.mustChangeBtnDisabled = false;
      }
    },

    // ── API ───────────────────────────────────────────────────────────────────

    async refreshAccounts() {
      try {
        const data = await apiGet('/api/accounts');
        this.accounts = data;
        this.lastUpdated = new Date().toLocaleTimeString();
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

    // ── Modal helpers ─────────────────────────────────────────────────────────

    openConnectModal() {
      this.$refs.connectDialog.showModal();
    },

    closeConnectModal() {
      this.$refs.connectDialog.close();
      // 重置 connect 暫態
      this.connectId = '';
      this.connectLabel = '';
      this.oauthCode = '';
      this.authorizeUrl = '';
      this.loginId = null;
      this.showOauthFlow = false;
      this.connectMsg = '';
      this.connectMsgType = '';
      this.startBtnDisabled = false;
    },

    openEnrollModal() {
      this.$refs.enrollDialog.showModal();
    },

    closeEnrollModal() {
      this.$refs.enrollDialog.close();
      // 重置 enroll 暫態
      this.enrollUser = '';
      this.enrollOneliner = '';
      this.enrollMsg = '';
      this.enrollMsgType = '';
      this.enrollBtnDisabled = false;
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
        // 成功後關閉 modal
        this.$nextTick(() => this.closeConnectModal());
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

      // 檢查認證狀態
      await this.checkAuth();
      this.authReady = true;

      if (this.authed) {
        if (this.mustChange) {
          this.$nextTick(() => {
            this.$refs.mustChangeDialog && this.$refs.mustChangeDialog.showModal();
          });
        } else {
          await this.refreshAccounts();
        }
      }

      // poll every 30s（只在已認證時抓資料）
      setInterval(async () => {
        if (this.authed) await this.refreshAccounts();
      }, 30000);

      // countdown tick every second
      this._countdownHandle = setInterval(() => {
        // trigger reactivity by reassigning accounts (shallow copy triggers Alpine watchers)
        // We only need the countdown text to update, so a no-op reassign works.
        this.accounts = this.accounts.slice();
      }, 1000);
    },
  }));
});
