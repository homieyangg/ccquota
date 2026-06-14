package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ccquota/ccquota/internal/alert"
	apipkg "github.com/ccquota/ccquota/internal/api"
	"github.com/ccquota/ccquota/internal/ingest"
	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/poller"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
	"github.com/ccquota/ccquota/internal/web"
)

func main() {
	dbPath := os.Getenv("CCQUOTA_DB")
	if dbPath == "" {
		dbPath = "ccquota.db"
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ccquota <login|serve|poll>")
		os.Exit(2)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	switch os.Args[1] {
	case "login":
		runLogin(s)
	case "serve":
		runServe(s)
	case "poll":
		runPoll(s)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runLogin(s *store.Store) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	id := fs.String("id", "", "account id (short handle, e.g. main)")
	label := fs.String("label", "", "human label (defaults to id)")
	fs.Parse(os.Args[2:])
	if *id == "" {
		log.Fatal("login: --id required")
	}
	lbl := *label
	if lbl == "" {
		lbl = *id
	}
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

func buildPoller(s *store.Store, n *alert.Notifier) *poller.Poller {
	return &poller.Poller{
		Store: s,
		Usage: &usage.Client{},
		OAuth: &oauth.Client{},
		OnReset: func(acct string, from, to float64) {
			log.Printf("RESET account=%s 7d %.0f%% -> %.0f%%", acct, from, to)
			if err := n.Reset(context.Background(), acct, from, to); err != nil {
				log.Printf("alert reset error: %v", err)
			}
		},
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

	if os.Getenv("CCQUOTA_ADMIN_PASSWORD") == "" {
		log.Printf("CCQUOTA_ADMIN_PASSWORD not set, auto-generated password: %s", apipkg.AdminPassword)
	}

	n := buildNotifier(s)
	p := buildPoller(s, n)
	ctx := context.Background()
	go func() {
		for {
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
	)
	mux.Handle("/api/", apiHandler)
	mux.Handle("/e/", apiHandler)
	mux.Handle("/healthz", apiHandler)

	ingestToken := os.Getenv("CCQUOTA_INGEST_TOKEN")
	if ingestToken != "" {
		log.Printf("ingest: enabled, listening at POST /v1/metrics")
	} else {
		log.Printf("ingest: disabled (CCQUOTA_INGEST_TOKEN not set)")
	}
	mux.Handle("/v1/metrics", ingest.New(s, ingestToken))

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

// runPoll 執行一次 poll 後退出。
func runPoll(s *store.Store) {
	n := buildNotifier(s)
	buildPoller(s, n).PollAll(context.Background())
}
