// Command storagej-server runs the blog HTTP API from a single binary:
// open the Pebble store, wire it through keyspace -> repository -> blog,
// serve httpapi. This is the "one touch" entry point the earlier phases
// were building toward — `go run ./cmd/storagej-server` and the blog is up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/examples/blog/blog"
	"github.com/uretgec/storagej/v2/examples/blog/httpapi"
	"github.com/uretgec/storagej/v2/pebble"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dataDir := flag.String("data", "./data", "Pebble data directory")
	tenant := flag.String("tenant", "default", "Tenant namespace served by this instance (ignored when -multi-tenant is set)")
	multiTenant := flag.Bool("multi-tenant", false,
		"serve every tenant via /t/{tenant}/posts, /t/{tenant}/pages instead of the single -tenant namespace; "+
			"no access control is added unless -api-keys is also set — see httpapi.MountMultiTenant's doc comment")
	apiKeys := flag.String("api-keys", "",
		"comma-separated tenant:key pairs for multi-tenant API key auth, e.g. \"acme:secret1,other:secret2\" "+
			"(only used with -multi-tenant; empty disables auth)")
	rateLimit := flag.Float64("rate-limit", 0,
		"per-tenant requests/sec for multi-tenant mode (0 disables rate limiting; only used with -multi-tenant)")
	rateLimitBurst := flag.Int("rate-limit-burst", 20, "per-tenant burst capacity for -rate-limit")
	metricsInterval := flag.Duration("metrics-interval", 45*time.Second,
		"periodic metrics logging interval (0 disables periodic logging; GET /health always works)")
	flag.Parse()

	opts := storagej.DefaultOptions()
	opts.OnEvent = logEvent
	opts.MetricsInterval = *metricsInterval
	opts.OnMetrics = logMetrics

	db, err := pebble.Open(*dataDir, opts)
	if err != nil {
		log.Fatalf("open store at %q: %v", *dataDir, err)
	}
	defer db.Close()

	api := httpapi.NewServer()
	if *multiTenant {
		keys, err := parseAPIKeys(*apiKeys)
		if err != nil {
			log.Fatalf("%v", err)
		}

		mountOpts := httpapi.MountOptions{}
		if len(keys) > 0 {
			mountOpts.Auth = httpapi.NewAPIKeyAuth(keys)
		}
		if *rateLimit > 0 {
			mountOpts.Limiter = httpapi.NewRateLimiter(*rateLimit, *rateLimitBurst)
		}

		httpapi.MountMultiTenant[blog.Post, *blog.Post](api, "posts", db, blog.PostType, mountOpts)
		httpapi.MountMultiTenant[blog.Page, *blog.Page](api, "pages", db, blog.PageType, mountOpts)

		switch {
		case mountOpts.Auth != nil && mountOpts.Limiter != nil:
			log.Printf("multi-tenant mode: API key auth for %d tenant(s), rate limit %.1f req/s (burst %d) per tenant",
				len(keys), *rateLimit, *rateLimitBurst)
		case mountOpts.Auth != nil:
			log.Printf("multi-tenant mode: API key auth for %d tenant(s), no rate limit", len(keys))
		case mountOpts.Limiter != nil:
			log.Printf("multi-tenant mode: no access control, rate limit %.1f req/s (burst %d) per tenant — see -api-keys",
				*rateLimit, *rateLimitBurst)
		default:
			log.Print("multi-tenant mode: no access control, no rate limit — see -api-keys and -rate-limit")
		}
	} else {
		httpapi.Mount(api, "posts", blog.NewPostRepository(db, *tenant))
		httpapi.Mount(api, "pages", blog.NewPageRepository(db, *tenant))
	}
	api.OnError = func(err error) { log.Printf("[ERROR] handler: %v", err) }
	api.Health = db // enables metrics in GET /health; db satisfies httpapi.HealthChecker

	srv := &http.Server{
		Addr:    *addr,
		Handler: api,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if *multiTenant {
			log.Printf("storagej-server listening on %s (multi-tenant, data=%q)", *addr, *dataDir)
		} else {
			log.Printf("storagej-server listening on %s (tenant=%q, data=%q)", *addr, *tenant, *dataDir)
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// logEvent forwards storagej background events (compactions, write stalls,
// WAL creation, background errors) to the stdlib logger. This is the same
// callback slot EXAMPLES.md wires to loggerj — swap this out for a real
// structured logger by replacing opts.OnEvent above, nothing else in this
// file needs to change.
func logEvent(e storagej.Event) {
	level := "INFO"
	switch e.Type {
	case storagej.EventBackgroundError, storagej.EventWriteStallBegin:
		level = "WARN"
	}
	log.Printf("[%s] storagej %s: %s %v", level, e.Type, e.Message, e.Fields)
}

// logMetrics logs a periodic point-in-time snapshot. It runs on
// storagej's own ticker goroutine (see Options.MetricsInterval), separate
// from GET /health, which computes a snapshot on demand instead.
func logMetrics(m storagej.MetricsSnapshot) {
	log.Printf("[METRICS] disk=%dMB cache_hit=%.1f%% files=%d size=%dMB compactions=%d wal=%dMB",
		m.DiskMB, m.CacheHitRatePercent, m.TotalFiles, m.TotalSizeMB, m.CompactionCount, m.WALSizeMB)
}

// parseAPIKeys parses "-api-keys"'s "tenant:key,tenant2:key2" format into a
// map. An empty string is valid and yields no keys (auth disabled).
func parseAPIKeys(s string) (map[string]string, error) {
	keys := make(map[string]string)
	if s == "" {
		return keys, nil
	}
	for _, pair := range strings.Split(s, ",") {
		tenant, key, ok := strings.Cut(pair, ":")
		if !ok || tenant == "" || key == "" {
			return nil, fmt.Errorf("invalid -api-keys entry %q, want tenant:key", pair)
		}
		keys[tenant] = key
	}
	return keys, nil
}
