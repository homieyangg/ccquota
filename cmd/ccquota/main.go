package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ccquota/ccquota/internal/alert"
	apipkg "github.com/ccquota/ccquota/internal/api"
	"github.com/ccquota/ccquota/internal/ingest"
	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/poller"
	"github.com/ccquota/ccquota/internal/secret"
	"github.com/ccquota/ccquota/internal/share"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
	"github.com/ccquota/ccquota/internal/web"
)

// version 由 release build 以 -ldflags "-X main.version=<tag>" 注入;本地 dev build 為 "dev"。
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ccquota <login|connect|set-token|detach|serve|poll|version>")
		os.Exit(2)
	}
	// version 不需要開 DB
	if os.Args[1] == "version" {
		fmt.Println(version)
		return
	}

	dbPath := os.Getenv("CCQUOTA_DB")
	if dbPath == "" {
		dbPath = "ccquota.db"
	}
	s, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	switch os.Args[1] {
	case "login":
		runLogin(s)
	case "set-token":
		runSetToken(s)
	case "connect":
		runConnect(s)
	case "detach":
		runDetach(s)
	case "serve":
		runServe(s)
	case "poll":
		runPoll(s)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

// defaultLabel 回傳 label，為空時 fallback 到 id。
func defaultLabel(label, id string) string {
	if label != "" {
		return label
	}
	return id
}

func runLogin(s *store.Store) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	id := fs.String("id", "", "account id (short handle, e.g. main)")
	label := fs.String("label", "", "human label (defaults to id)")
	fs.Parse(os.Args[2:])
	if *id == "" {
		log.Fatal("login: --id required")
	}
	lbl := defaultLabel(*label, *id)
	p, err := oauth.NewPKCE()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Open this URL, authorize, then paste the code shown:")
	fmt.Println()
	fmt.Println("  " + oauth.AuthorizeURL(p))
	fmt.Println()
	fmt.Print("code> ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	code := strings.TrimSpace(line)

	c := &oauth.Client{}
	tok, err := c.ExchangeCode(context.Background(), code, p)
	if err != nil {
		log.Fatalf("exchange failed: %v", err)
	}
	if err := s.UpsertAccount(store.Account{
		ID: *id, Label: lbl,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ExpiresAt: time.Now().Unix() + tok.ExpiresIn,
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("account %q connected.\n", *id)
}

// runSetToken 直接把現成的 OAuth token 寫進 DB,免走網頁 OAuth。
// 來源優先序:--access-token > --creds 檔 > ~/.claude/.credentials.json。
// --no-refresh 會把 expires_at 設成遠未來,讓本 poller 永不主動 refresh
// (適合外部已有 refresher 在輪替同一個帳號 token 的情境)。
func runSetToken(s *store.Store) {
	fs := flag.NewFlagSet("set-token", flag.ExitOnError)
	id := fs.String("id", "", "account id (short handle, e.g. main)")
	label := fs.String("label", "", "human label (defaults to id)")
	creds := fs.String("creds", "", "path to Claude credentials json (default ~/.claude/.credentials.json)")
	accessTok := fs.String("access-token", "", "access token (overrides --creds)")
	refreshTok := fs.String("refresh-token", "", "refresh token (used with --access-token)")
	expiresIn := fs.Int64("expires-in", 0, "seconds until access token expiry (used with --access-token)")
	noRefresh := fs.Bool("no-refresh", false, "store a far-future expiry so this poller never refreshes the token itself")
	fs.Parse(os.Args[2:])
	if *id == "" {
		log.Fatal("set-token: --id required")
	}
	lbl := defaultLabel(*label, *id)

	now := time.Now().Unix()
	var at, rt string
	var expiresAt int64
	if *accessTok != "" {
		at, rt = *accessTok, *refreshTok
		if *expiresIn > 0 {
			expiresAt = now + *expiresIn
		} else {
			expiresAt = now + 3600
		}
	} else {
		path := *creds
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				log.Fatalf("set-token: cannot resolve home dir: %v", err)
			}
			path = filepath.Join(home, ".claude", ".credentials.json")
		}
		a, r, expMs, err := readClaudeCreds(path)
		if err != nil {
			log.Fatalf("set-token: %v", err)
		}
		at, rt, expiresAt = a, r, expMs/1000
	}
	if at == "" {
		log.Fatal("set-token: no access token found")
	}
	if *noRefresh {
		expiresAt = now + 3650*86400 // ~10 年,poller 永不因接近到期而 refresh
	}

	if err := s.UpsertAccount(store.Account{
		ID: *id, Label: lbl,
		AccessToken: at, RefreshToken: rt, ExpiresAt: expiresAt,
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("account %q token updated.\n", *id)
}

// readClaudeCreds 解析 Claude Code 的 ~/.claude/.credentials.json。
// expiresAt 欄位是毫秒;回傳值原樣保留毫秒,由呼叫端換算。
func readClaudeCreds(path string) (access, refresh string, expiresAtMs int64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", "", 0, fmt.Errorf("parse %s: %w", path, err)
	}
	o := doc.ClaudeAiOauth
	return o.AccessToken, o.RefreshToken, o.ExpiresAt, nil
}

// envFloat64 讀取 env 變數並解析為 float64，失敗時回傳 defaultVal。
func envFloat64(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using default %.0f", key, v, defaultVal)
		return defaultVal
	}
	return f
}

// envInt64 讀取 env 變數並解析為 int64，失敗時回傳 defaultVal。
func envInt64(key string, defaultVal int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using default %d", key, v, defaultVal)
		return defaultVal
	}
	return i
}

// buildNotifier 從 env 組裝 Notifier。無 sink 設定時回傳 no-op Notifier。
func buildNotifier(s *store.Store) *alert.Notifier {
	cfg := alert.Config{
		Lang:           os.Getenv("CCQUOTA_ALERT_LANG"),
		WeeklyWarn:     envFloat64("CCQUOTA_WEEKLY_WARN", 75),
		WeeklyCrit:     envFloat64("CCQUOTA_WEEKLY_CRIT", 90),
		FiveHourCrit:   envFloat64("CCQUOTA_FIVEHOUR_CRIT", 95),
		PollerStaleSec: envInt64("CCQUOTA_POLLER_STALE_SEC", 1800),
	}

	var sinks []alert.Sink

	tgToken := os.Getenv("CCQUOTA_TELEGRAM_TOKEN")
	tgChat := os.Getenv("CCQUOTA_TELEGRAM_CHAT")
	if tgToken != "" && tgChat != "" {
		sinks = append(sinks, &alert.TelegramSink{
			Token:  tgToken,
			ChatID: tgChat,
		})
		log.Printf("alert: Telegram sink enabled (chat=%s)", tgChat)
	}

	webhookURL := os.Getenv("CCQUOTA_WEBHOOK_URL")
	if webhookURL != "" {
		sinks = append(sinks, &alert.WebhookSink{URL: webhookURL})
		log.Printf("alert: webhook sink enabled (%s)", webhookURL)
	}

	if len(sinks) == 0 {
		log.Printf("alert: no sinks configured, notifications disabled")
	}

	return alert.NewNotifier(cfg, s, sinks...)
}

// loadCipher 從 env 或 dataDir 下的 keyfile 取得加密金鑰。
func loadCipher() *secret.Cipher {
	dbPath := os.Getenv("CCQUOTA_DB")
	if dbPath == "" {
		dbPath = "ccquota.db"
	}
	keyfile := filepath.Join(filepath.Dir(dbPath), "secret.key")
	key, err := secret.LoadKey(os.Getenv("CCQUOTA_SECRET_KEY"), keyfile)
	if err != nil {
		log.Fatalf("load secret key: %v", err)
	}
	c, err := secret.New(key)
	if err != nil {
		log.Fatalf("init cipher: %v", err)
	}
	// 用同一把持久化金鑰簽 session,讓 session 跨重啟/自我更新存活
	apipkg.SetSessionSecret(key)
	return c
}

// buildNotifierFromStore 每輪呼叫:從 DB 讀 enabled 頻道(解密 secret)+ 門檻建 Notifier。
// DB 沒有任何頻道時 fallback 到 env 設定(向後相容)。
func buildNotifierFromStore(s *store.Store, c *secret.Cipher) *alert.Notifier {
	channels, err := s.ListChannels()
	if err != nil {
		log.Printf("notifier: list channels: %v", err)
	}
	var sinks []alert.Sink
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		var raw map[string]string
		if err := json.Unmarshal([]byte(ch.Config), &raw); err != nil {
			log.Printf("notifier: bad channel %d config: %v", ch.ID, err)
			continue
		}
		dec := make(map[string]string, len(raw))
		for k, v := range raw {
			pt, err := c.Decrypt(v)
			if err != nil {
				log.Printf("notifier: decrypt channel %d field %s: %v", ch.ID, k, err)
				pt = ""
			}
			dec[k] = pt
		}
		sink, err := alert.BuildSink(ch.Type, dec)
		if err != nil {
			log.Printf("notifier: %v", err)
			continue
		}
		sinks = append(sinks, sink)
	}

	th, err := s.GetAlertThresholds()
	if err != nil {
		log.Printf("notifier: thresholds: %v", err)
	}
	cfg := alert.Config{
		Lang:            os.Getenv("CCQUOTA_LANG"),
		WeeklyWarn:      th.SevenDayWarn,
		WeeklyCrit:      th.SevenDayCrit,
		FiveHourCrit:    th.FiveHourCrit,
		PollerStaleSec:  envInt64("CCQUOTA_POLLER_STALE_SEC", 1800),
		UserShareNotify: th.UserShareNotify,
		UserShareWarn:   th.UserShareWarn,
		UserShareCrit:   th.UserShareCrit,
	}

	if len(sinks) == 0 {
		return buildNotifier(s) // fallback 到 env
	}
	return alert.NewNotifier(cfg, s, sinks...)
}

func buildPoller(s *store.Store) *poller.Poller {
	return &poller.Poller{
		Store:      s,
		Usage:      &usage.Client{},
		OAuth:      &oauth.Client{},
		RefreshCmd: claudeDoctorRefresh,
	}
}

// findClaude 找本機 claude CLI(PATH 或常見安裝路徑)。
func findClaude() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local/bin/claude"),
		"/usr/local/bin/claude",
		"/opt/homebrew/bin/claude",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// claudeLoggedIn 檢查 claude CLI 是否已登入(讓 connect 可重入,已登入就不再 login)。
func claudeLoggedIn(bin string) bool {
	out, err := exec.Command(bin, "auth", "status", "--json").Output()
	if err != nil {
		return false
	}
	var st struct {
		LoggedIn bool `json:"loggedIn"`
	}
	_ = json.Unmarshal(out, &st)
	return st.LoggedIn
}

// claudeCredsPath 回傳 claude CLI 的 creds 檔路徑。
func claudeCredsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".credentials.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", ".credentials.json")
}

// claudeDoctorRefresh 跑 `claude doctor` 觸發本機 CLI 在快到期時免費 refresh token
// （不叫 model、不吃 Agent SDK credit)。doctor 是 TUI 會 hang,refresh 啟動就完成,timeout 砍掉即可。
func claudeDoctorRefresh(ctx context.Context) error {
	bin := findClaude()
	if bin == "" {
		return fmt.Errorf("claude CLI not found")
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "doctor")
	_ = cmd.Run() // 被 timeout 砍是預期的,refresh 已完成
	return nil
}

// runConnect:在 server 上一鍵接帳號(CLI-backed)。
// 跑 claude auth login(使用者瀏覽器授權 + 貼 code),然後把帳號設成「讀本機 creds」模式,
// poller 之後直接讀那顆 token 打 usage、快到期自動 doctor refresh。不碰被限流的 token endpoint。
func runConnect(s *store.Store) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	id := fs.String("id", "main", "account id")
	label := fs.String("label", "", "human label (defaults to id)")
	fs.Parse(os.Args[2:])
	lbl := defaultLabel(*label, *id)

	bin := findClaude()
	if bin == "" {
		log.Fatal("connect: 找不到 claude CLI。請先安裝 Claude Code,再重跑 ccquota connect。")
	}

	if claudeLoggedIn(bin) {
		fmt.Println("claude 已登入,直接接上帳號(略過 login)。")
	} else {
		fmt.Println("即將執行 claude auth login:請依畫面在瀏覽器授權、把 code 貼回來。")
		cmd := exec.Command(bin, "auth", "login")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("connect: claude auth login 失敗: %v", err)
		}
	}

	credsPath := claudeCredsPath()
	if _, err := os.Stat(credsPath); err != nil {
		log.Fatalf("connect: 登入後找不到 creds 檔 %s: %v", credsPath, err)
	}
	if err := s.UpsertAccount(store.Account{ID: *id, Label: lbl, CredsPath: credsPath}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ 帳號 %q 已接上(CLI-backed,creds: %s)\n", *id, credsPath)
	fmt.Println("  ccquota 會直接讀它打 usage,並在快到期時自動 refresh(免費,不叫 model)。")
}

// resetCallback 回傳 poller 重置事件處理函式:尊重 ResetNotify 開關,
// 每次都從 DB 重讀最新頻道設定再送通知。
func resetCallback(s *store.Store, c *secret.Cipher) func(string, float64, float64) {
	return func(acct string, from, to float64) {
		th, _ := s.GetAlertThresholds()
		if !th.ResetNotify {
			return
		}
		log.Printf("RESET account=%s 7d %.0f%% -> %.0f%%", acct, from, to)
		if err := buildNotifierFromStore(s, c).Reset(context.Background(), acct, from, to); err != nil {
			log.Printf("alert reset error: %v", err)
		}
	}
}

func runServe(s *store.Store) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":11451", "HTTP listen address")
	interval := fs.Duration("interval", 5*time.Minute, "poll interval (>=180s)")
	fs.Parse(os.Args[2:])
	if *interval < 180*time.Second {
		*interval = 180 * time.Second
	}

	cipher := loadCipher()
	p := buildPoller(s)
	p.OnReset = resetCallback(s, cipher)
	ctx := context.Background()
	go func() {
		for {
			// 每輪重建 notifier，確保頻道設定即時生效。
			n := buildNotifierFromStore(s, cipher)
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			p.PollAll(pollCtx)
			cancel()

			// 對每個帳號做 stale / threshold 檢查
			accts, err := s.ListAccounts()
			if err != nil {
				log.Printf("serve: list accounts: %v", err)
			} else {
				now := time.Now().Unix()
				for _, a := range accts {
					r, ok, err := s.LatestReading(a.ID)
					if err != nil || !ok {
						continue
					}
					age := now - r.TS
					if age >= n.PollerStaleSec() {
						if err := n.Stale(ctx, a.ID, age); err != nil {
							log.Printf("alert stale error: %v", err)
						}
					} else {
						if err := n.Thresholds(ctx, a.ID, r.SevenDay, r.FiveHour, r.SevenDayResetsAt, r.FiveHourResetsAt); err != nil {
							log.Printf("alert thresholds error: %v", err)
						}
						// per-user 平分額度 advisory(預設關;notifier 內部會檢查啟用與 resets_at)。
						// 與 dashboard 共用 share.Compute / SinceTS,確保視窗一致、數字不分岔。
						sinceTS := share.SinceTS(r, true, now)
						if res, cerr := share.Compute(s, a.ID, sinceTS, r.SevenDay); cerr == nil {
							ur := make([]alert.UserShareReading, 0, len(res.Shares))
							for _, sh := range res.Shares {
								ur = append(ur, alert.UserShareReading{User: sh.User, SharePct: sh.SharePct, Cost: sh.Cost})
							}
							if err := n.UserShareThresholds(ctx, a.ID, ur, res.PerUserBudget, r.SevenDayResetsAt); err != nil {
								log.Printf("alert user-share error: %v", err)
							}
						}
					}
				}
			}

			time.Sleep(*interval)
		}
	}()

	staleSec := envInt64("CCQUOTA_POLLER_STALE_SEC", 1800)
	mux := http.NewServeMux()
	oc := &oauth.Client{}
	apiHandler := apipkg.New(s, oc, staleSec,
		os.Getenv("CCQUOTA_INGEST_TOKEN"),
		os.Getenv("CCQUOTA_PUBLIC_URL"),
		cipher,
		version,
	)
	mux.Handle("/api/", apiHandler)
	mux.Handle("/e/", apiHandler)
	mux.Handle("/healthz", apiHandler)

	// 只有這次啟動真的自動產生密碼（store 原本沒有 hash）才印出，避免重啟時誤導。
	if apipkg.SeededAutoPassword {
		log.Printf("auto-generated admin password (shown once, change it on first login): %s", apipkg.AdminPassword)
	}

	ingestToken := os.Getenv("CCQUOTA_INGEST_TOKEN")
	if ingestToken != "" {
		log.Printf("ingest: enabled, listening at POST /v1/metrics")
	} else {
		log.Printf("ingest: disabled (CCQUOTA_INGEST_TOKEN not set)")
	}
	mux.Handle("/v1/metrics", ingest.New(s, ingestToken))
	// client 讀本機現成 access token 後推回來,server 用它統一輪詢 usage
	// (每帳號每週期一次,不會 N 倍 429),且永不碰被限流的 token endpoint。
	mux.Handle("/v1/token", ingest.NewTokenHandler(s, ingestToken))
	// 備用:client 也可改自行打 usage、只推結果%(token 不離開本機)。
	mux.Handle("/v1/usage", ingest.NewUsageHandler(s, ingestToken, resetCallback(s, cipher)))

	mux.Handle("/", web.Handler())

	log.Printf("ccquota listening on %s, poll every %s", *addr, *interval)
	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// runDetach 把帳號切成「client 餵 token」模式：清掉 refresh token，poller 之後就
// 不再自行 refresh（避開被限流的 token endpoint）。保留 access token，client 會用
// POST /v1/token 持續餵新的,server 照常用它輪詢 usage。
func runDetach(s *store.Store) {
	fs := flag.NewFlagSet("detach", flag.ExitOnError)
	id := fs.String("id", "", "account id")
	fs.Parse(os.Args[2:])
	if *id == "" {
		log.Fatal("detach: --id required")
	}
	a, err := s.GetAccount(*id)
	if err != nil {
		log.Fatalf("detach: account %s: %v", *id, err)
	}
	a.RefreshToken = ""
	if err := s.UpsertAccount(a); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("account %s detached: 不再自行 refresh,access token 改由 POST /v1/token 餵入\n", *id)
}

// runPoll 執行一次 poll 後退出。
func runPoll(s *store.Store) {
	p := buildPoller(s)
	p.OnReset = resetCallback(s, loadCipher())
	p.PollAll(context.Background())
}
