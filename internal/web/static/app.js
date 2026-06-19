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
  const v = window.__ASSET_V ? `?v=${window.__ASSET_V}` : '';
  const res = await fetch(`/i18n/${lang}.json${v}`);
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

// 解析回應；非 2xx 時用後端 error 欄位（沒有則用狀態碼）丟錯。
async function apiResult(res) {
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error || `${res.status}`);
  }
  return res.json();
}

async function apiPost(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return apiResult(res);
}

async function apiPut(path, body) {
  const res = await fetch(path, {
    method: 'PUT', credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return apiResult(res);
}

async function apiDelete(path) {
  const res = await fetch(path, { method: 'DELETE', credentials: 'include' });
  return apiResult(res);
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

// ── Series area chart (canvas) ────────────────────────────────────────────────

// _chartTooltip 取得(必要時建立)共用的浮動 tooltip 元素。
function _chartTooltip() {
  let el = document.getElementById('cc-chart-tooltip');
  if (!el) {
    el = document.createElement('div');
    el.id = 'cc-chart-tooltip';
    el.className = 'chart-tooltip';
    document.body.appendChild(el);
  }
  return el;
}

// _fmtBucketTime 把 epoch 秒格式化成 MM/DD HH:MM(本地時間)。
function _fmtBucketTime(ts) {
  const d = new Date(ts * 1000);
  const p = n => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// drawSeriesChart 畫面積 + 線時序圖;滑鼠移上去顯示十字線、節點與數值 tooltip。
// fmt(value) 回傳 tooltip 顯示的數值字串。
function drawSeriesChart(canvas, points, valueFn, color, fmt) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || 300;
  const h = 64;
  canvas.width = w * dpr; canvas.height = h * dpr;
  const ctx = canvas.getContext('2d'); ctx.scale(dpr, dpr);
  const pts = points || [];
  const vals = pts.map(valueFn);
  if (vals.length < 2) {
    ctx.fillStyle = '#a1a1aa'; ctx.font = '10px sans-serif'; ctx.fillText('N/A', w / 2 - 6, h / 2);
    canvas.onmousemove = null; canvas.onmouseleave = null;
    return;
  }
  const max = Math.max(...vals, 0.0000001);
  const pad = 4, yw = h - pad * 2, xw = w - pad * 2;
  const x = i => pad + (i / (vals.length - 1)) * xw;
  const y = v => pad + yw - (v / max) * yw;

  const render = hoverIdx => {
    ctx.clearRect(0, 0, w, h);
    // area
    ctx.beginPath(); ctx.moveTo(x(0), h - pad);
    vals.forEach((v, i) => ctx.lineTo(x(i), y(v)));
    ctx.lineTo(x(vals.length - 1), h - pad); ctx.closePath();
    ctx.fillStyle = color + '22'; ctx.fill();
    // line
    ctx.beginPath();
    vals.forEach((v, i) => i === 0 ? ctx.moveTo(x(i), y(v)) : ctx.lineTo(x(i), y(v)));
    ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.stroke();
    // hover 十字線 + 節點
    if (hoverIdx != null) {
      const hx = x(hoverIdx), hy = y(vals[hoverIdx]);
      ctx.beginPath(); ctx.moveTo(hx, pad); ctx.lineTo(hx, h - pad);
      ctx.strokeStyle = 'rgba(0,0,0,0.18)'; ctx.lineWidth = 1; ctx.stroke();
      ctx.beginPath(); ctx.arc(hx, hy, 3, 0, Math.PI * 2);
      ctx.fillStyle = color; ctx.fill();
      ctx.strokeStyle = '#fff'; ctx.lineWidth = 1.5; ctx.stroke();
    }
  };
  render(null);

  const tip = _chartTooltip();
  canvas.onmousemove = e => {
    const rect = canvas.getBoundingClientRect();
    let i = Math.round((e.clientX - rect.left - pad) / xw * (vals.length - 1));
    i = Math.max(0, Math.min(vals.length - 1, i));
    render(i);
    tip.style.display = 'block';
    tip.innerHTML = '<strong>' + (fmt ? fmt(vals[i]) : vals[i].toFixed(2)) + '</strong><br>' + _fmtBucketTime(pts[i].ts);
    tip.style.left = (e.clientX + 12) + 'px';
    tip.style.top = (e.clientY - 10) + 'px';
  };
  canvas.onmouseleave = () => { render(null); tip.style.display = 'none'; };
}

// ── Alpine component ──────────────────────────────────────────────────────────

// 每次回傳新物件，避免初始值與重置共用同一個 reference。
function defaultNewChannel() {
  return { type: 'telegram', enabled: true, config: { bot_token: '', chat_id: '', lang: '' } };
}

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

    // per-user series data and range selection
    userRanges: {},
    userSeries: {},
    copyMsg: '',

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

    // notifications
    settingsOpen: false,
    editingChannelId: null, // 展開編輯中的頻道 id；null = 全收合
    showAddChannel: false,  // 是否展開「新增頻道」表單
    channels: [],
    thresholds: { SevenDayWarn: 75, SevenDayCrit: 90, FiveHourCrit: 95, ResetNotify: true, UserShareNotify: false, UserShareWarn: 150, UserShareCrit: 250 },
    newChannel: defaultNewChannel(),
    notifMsg: '',
    notifMsgType: '',

    // self-update
    updateInfo: { current: '', latest: '', update_available: false, notes_url: '' },
    updating: false,

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

    // token 模式 = $ 還沒累積夠(冷啟動),但 token 軌已能反推。
    isTokenMode(cost) {
      return !!cost && !(cost.weekly_budget_usd > 0) && cost.token_weekly_budget > 0;
    },

    fmtTokens(n) {
      if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
      if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
      if (n >= 1e3) return Math.round(n / 1e3) + 'k';
      return Math.round(n).toString();
    },

    // 反推週額度顯示:$ 夠用顯示 $,冷啟動只有 token 時顯示 token 估算。
    budgetText(cost) {
      if (!cost) return '$0.00';
      if (cost.weekly_budget_usd > 0) return '$' + cost.weekly_budget_usd.toFixed(2);
      if (cost.token_weekly_budget > 0) return this.fmtTokens(cost.token_weekly_budget) + ' tok';
      return '$0.00';
    },

    // 額度使用率:$ 模式用 share_pct,token 模式改用 token 平分佔比。
    userBudgetPct(cost, u) {
      return this.isTokenMode(cost) ? (u.token_budget_pct || 0) : (u.share_pct || 0);
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

    // ── self-update ────────────────────────────────────────────────────────────

    async checkVersion(force) {
      try {
        this.updateInfo = await apiGet('/api/version' + (force ? '?check=1' : ''));
      } catch (e) { /* 查不到版本不影響使用 */ }
    },

    async doUpdate() {
      if (!confirm(this.t('update_now') + ' ' + (this.updateInfo.latest || '') + ' ?')) return;
      this.updating = true;
      try {
        await apiPost('/api/update', {});
      } catch (e) {
        // 連線可能在 server re-exec 時中斷,屬正常,照樣等它重啟
      }
      // 等 server 換 binary + 重啟,再重新整理
      setTimeout(() => location.reload(), 5000);
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
      this.notifMsg = '';
      this.editingChannelId = null;
      this.showAddChannel = false;
      this.newChannel = defaultNewChannel();
      this.loadNotifications();
      this.settingsOpen = true;
      window.scrollTo(0, 0);
    },

    closeSettingsModal() {
      this.settingsOpen = false;
    },

    // 帳號額度告警是否啟用:後端用 101 代表關閉(無 enabled 旗標),這裡用門檻值反推開關。
    get accountAlertsOn() {
      return this.thresholds.SevenDayWarn <= 100 || this.thresholds.SevenDayCrit <= 100;
    },

    // 切換帳號額度告警:關 → 三個門檻設 101(後端視為停用);開 → 還原預設(若目前是停用值)。
    toggleAccountAlerts(on) {
      if (on) {
        if (this.thresholds.SevenDayWarn > 100) this.thresholds.SevenDayWarn = 75;
        if (this.thresholds.SevenDayCrit > 100) this.thresholds.SevenDayCrit = 90;
        if (this.thresholds.FiveHourCrit > 100) this.thresholds.FiveHourCrit = 95;
      } else {
        this.thresholds.SevenDayWarn = 101;
        this.thresholds.SevenDayCrit = 101;
        this.thresholds.FiveHourCrit = 101;
      }
      this.saveThresholds();
    },

    // 頻道啟用 toggle:立即存(不必再按儲存)。
    async toggleChannel(ch) {
      await this.saveChannel(ch);
    },

    startEditChannel(ch) {
      ch.config.bot_token = '';
      this.editingChannelId = ch.id;
    },

    beginAddChannel() {
      this.newChannel = defaultNewChannel();
      this.showAddChannel = true;
    },

    channelDetail(ch) {
      return ch.type === 'telegram' ? (ch.config.chat_id || '') : (ch.config.url || '');
    },

    // ── Notifications ─────────────────────────────────────────────────────────

    async loadNotifications() {
      try {
        const data = await apiGet('/api/notifications');
        this.channels = data.channels || [];
        // 清空 bot_token，避免將遮罩值回存；後端收到空值時保留原 token
        // lang 缺值正規化成 ''（下拉顯示「沿用伺服器預設」，存回也忠實 round-trip）
        for (const ch of this.channels) {
          if (ch.type === 'telegram' && ch.config) {
            ch.config.bot_token = '';
            if (ch.config.lang == null) ch.config.lang = '';
          }
        }
        if (data.thresholds) this.thresholds = data.thresholds;
      } catch (e) { console.error('load notifications', e); }
    },

    async addChannel() {
      this.notifMsg = '';
      try {
        await apiPost('/api/notifications/channels', this.newChannel);
        this.newChannel = defaultNewChannel();
        this.showAddChannel = false;
        await this.loadNotifications();
        this.notifMsg = this.t('notif_saved'); this.notifMsgType = 'success';
      } catch (e) { this.notifMsg = e.message; this.notifMsgType = 'error'; }
    },

    async saveChannel(ch) {
      try {
        await apiPut(`/api/notifications/channels/${ch.id}`, { enabled: ch.enabled, config: ch.config });
        this.editingChannelId = null;
        await this.loadNotifications();
        this.notifMsg = this.t('notif_saved'); this.notifMsgType = 'success';
      } catch (e) { this.notifMsg = e.message; this.notifMsgType = 'error'; }
    },

    async deleteChannel(ch) {
      try { await apiDelete(`/api/notifications/channels/${ch.id}`); await this.loadNotifications(); }
      catch (e) { this.notifMsg = e.message; this.notifMsgType = 'error'; }
    },

    async testChannel(ch) {
      this.notifMsg = '';
      try {
        const r = await apiPost(`/api/notifications/channels/${ch.id}/test`, {});
        if (r.status === 'ok') { this.notifMsg = this.t('notif_test_ok'); this.notifMsgType = 'success'; }
        else { this.notifMsg = this.t('notif_test_fail') + ': ' + (r.error || ''); this.notifMsgType = 'error'; }
      } catch (e) { this.notifMsg = this.t('notif_test_fail') + ': ' + e.message; this.notifMsgType = 'error'; }
    },

    async saveThresholds() {
      // 平分額度告警:warn 必須小於 crit,否則 warn 永遠不觸發。
      if (this.thresholds.UserShareNotify &&
          this.thresholds.UserShareWarn >= this.thresholds.UserShareCrit) {
        this.notifMsg = this.t('notif_share_order_err'); this.notifMsgType = 'error';
        return;
      }
      try {
        await apiPut('/api/notifications/thresholds', this.thresholds);
        this.notifMsg = this.t('notif_saved'); this.notifMsgType = 'success';
      } catch (e) { this.notifMsg = e.message; this.notifMsgType = 'error'; }
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
        // kick off per-user series loads
        for (const acct of this.accounts) {
          for (const u of (acct.cost?.users || [])) this.loadUserSeries(acct.id, u.user);
        }
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

    // ── Per-user series ───────────────────────────────────────────────────────

    rangeFor(user) { return this.userRanges[user] || '24h'; },

    async setUserRange(account, user, range) {
      this.userRanges = { ...this.userRanges, [user]: range };
      await this.loadUserSeries(account, user);
    },

    async loadUserSeries(account, user) {
      try {
        const r = this.rangeFor(user);
        const data = await apiGet(`/api/user-series?account=${encodeURIComponent(account)}&user=${encodeURIComponent(user)}&range=${r}`);
        this.userSeries = { ...this.userSeries, [user]: data };
        this.$nextTick(() => this.drawUserCharts(user));
      } catch (e) { console.error('user-series', e); }
    },

    drawUserCharts(user) {
      const data = this.userSeries[user];
      if (!data) return;
      const bs = data.bucket_sec || 600;
      const costCanvas = document.getElementById('cost-' + user);
      const tokCanvas = document.getElementById('tok-' + user);
      if (costCanvas) drawSeriesChart(costCanvas, data.points, p => p.cost_usd / (bs / 3600), '#18181b', v => '$' + v.toFixed(2) + '/hr');
      if (tokCanvas) drawSeriesChart(tokCanvas, data.points, p => p.tokens / bs, '#3b82f6', v => Math.round(v).toLocaleString() + ' tok/s');
    },

    async copyInstall(account, user) {
      try {
        const data = await apiPost('/api/enroll', { account, user });
        await navigator.clipboard.writeText(`bash <(curl -fsSL -A ccquota-setup ${data.url})`);
        this.copyMsg = user; setTimeout(() => { this.copyMsg = ''; }, 1500);
      } catch (e) { console.error('copy install', e); }
    },

    async removeUser(account, user) {
      if (!confirm(this.t('delete_user_confirm'))) return;
      try {
        await apiDelete(`/api/users?account=${encodeURIComponent(account)}&user=${encodeURIComponent(user)}`);
        await this.refreshAccounts();
      } catch (e) { console.error('delete user', e); }
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
        // -A 自報 UA:有些反代/WAF 會擋預設 curl UA(例如擋 bot),帶一個明確 UA 才抓得到腳本。
        this.enrollOneliner = `bash <(curl -fsSL -A ccquota-setup ${data.url})`;
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
          this.checkVersion(false);
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
