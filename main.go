package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

// compNameRe validates competition names extracted from URLs and filenames.
// Only letters, digits, hyphens, and underscores are allowed.
var compNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ── Competition ───────────────────────────────────────────────────────────────

// Competition holds all runtime state for a single configured competition.
type Competition struct {
	name        string
	displayName string // COMP_NAME from config, falls back to name
	website     string // COMP_WEBSITE from config, optional
	hub         *Hub
	bot         *Bot
	glideURL    string
	pinCounts   map[string]int
	pinMu       sync.Mutex
	activeConns int32 // accessed via sync/atomic; counts live SSE connections
}

// ── Registry ──────────────────────────────────────────────────────────────────

// Registry is the top-level HTTP handler. It lazily loads competitions on
// first access and tears them down 5 minutes after the last viewer leaves
// (if the config file was removed).
type Registry struct {
	mu    sync.RWMutex
	comps map[string]*Competition
}

// ServeHTTP dispatches every request to the right competition or the root listing.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	if path == "/" || path == "" {
		r.serveRoot(w, req)
		return
	}

	// Extract /{compName}[/rest] from the path.
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	compName := parts[0]
	rest := "/"
	if len(parts) > 1 {
		rest = "/" + parts[1]
	}

	if !compNameRe.MatchString(compName) {
		http.NotFound(w, req)
		return
	}

	// Redirect /compName → /compName/ for clean relative URL resolution.
	if rest == "/" && !strings.HasSuffix(path, "/") {
		http.Redirect(w, req, path+"/", http.StatusMovedPermanently)
		return
	}

	comp, err := r.getOrLoad(compName)
	if err != nil {
		if os.IsNotExist(err) {
			http.Redirect(w, req, "/", http.StatusFound)
		} else {
			log.Printf("error loading competition %q: %v", compName, err)
			http.Error(w, "failed to load competition", http.StatusInternalServerError)
		}
		return
	}

	switch rest {
	case "/events":
		sseHandler(comp)(w, req)
	case "/pinned":
		pinnedHandler(comp.bot)(w, req)
	case "/config":
		configHandler(comp.glideURL, comp.bot.ChannelURL(), comp.displayName, comp.website)(w, req)
	default:
		http.StripPrefix("/"+compName, http.FileServer(http.Dir("frontend"))).ServeHTTP(w, req)
	}
}

// compInfo holds the minimal fields needed to build the root listing without
// fully loading a competition (no bot, no hub).
type compInfo struct {
	slug        string // filename stem = URL path segment
	displayName string // COMP_NAME from config, falls back to slug
	website     string // COMP_WEBSITE from config, optional
	published   bool   // PUBLISHED=true required to appear in the listing
}

// readCompInfo reads COMP_NAME, COMP_WEBSITE and PUBLISHED from a comp env file.
func readCompInfo(slug string) compInfo {
	info := compInfo{slug: slug, displayName: slug}
	env, err := godotenv.Read(filepath.Join("comps", slug+".env"))
	if err != nil {
		return info
	}
	if v := env["COMP_NAME"]; v != "" {
		info.displayName = v
	}
	if v := env["COMP_WEBSITE"]; v != "" {
		info.website = v
	}
	info.published = strings.EqualFold(env["PUBLISHED"], "true")
	return info
}

// serveRoot scans comps/ and either redirects (1 comp), lists (many), or shows empty state.
func (r *Registry) serveRoot(w http.ResponseWriter, req *http.Request) {
	entries, _ := os.ReadDir("comps")
	var comps []compInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".env") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".env")
		if !compNameRe.MatchString(slug) {
			continue
		}
		if info := readCompInfo(slug); info.published {
			comps = append(comps, info)
		}
	}

	switch len(comps) {
	case 0:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, listingPage, `<p class="empty">No competitions configured yet.<br>Add a <code>.env</code> file to the <code>comps/</code> directory.</p>`)
	case 1:
		http.Redirect(w, req, "/"+comps[0].slug+"/", http.StatusFound)
	default:
		var sb strings.Builder
		sb.WriteString(`<ul class="comp-list">`)
		for _, c := range comps {
			label := escapeHTMLAttr(c.displayName)
			sb.WriteString(fmt.Sprintf(`<li><a href="/%s/">%s</a></li>`, c.slug, label))
		}
		sb.WriteString(`</ul>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, listingPage, sb.String())
	}
}

func escapeHTMLAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// getOrLoad returns the named competition from the registry, loading it from
// comps/{name}.env on first access. Returns os.ErrNotExist if the file is absent.
func (r *Registry) getOrLoad(name string) (*Competition, error) {
	// Fast path: already loaded.
	r.mu.RLock()
	c := r.comps[name]
	r.mu.RUnlock()
	if c != nil {
		return c, nil
	}

	envPath := filepath.Join("comps", name+".env")
	if _, err := os.Stat(envPath); err != nil {
		return nil, err
	}

	// Slow path: load under write lock (double-check to avoid duplicate starts).
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.comps[name]; c != nil {
		return c, nil
	}

	env, err := godotenv.Read(envPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", envPath, err)
	}

	comp, err := startCompetition(name, env)
	if err != nil {
		return nil, err
	}

	comp.hub.onIdle = r.idleHandler(name, envPath, comp)
	r.comps[name] = comp
	log.Printf("competition %q loaded", name)
	return comp, nil
}

// idleHandler returns the function called when the last SSE client disconnects.
// After 5 minutes it checks whether the config file was removed; if so it tears
// down the competition and removes it from the registry.
func (r *Registry) idleHandler(name, envPath string, comp *Competition) func() {
	return func() {
		time.AfterFunc(5*time.Minute, func() {
			// If the file still exists, keep the competition loaded.
			if _, err := os.Stat(envPath); err == nil {
				return
			}
			// If a viewer reconnected in the meantime, skip teardown.
			if atomic.LoadInt32(&comp.activeConns) > 0 {
				return
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.comps[name] != comp {
				return // already replaced by a reload
			}
			comp.bot.Close()
			close(comp.hub.quit)
			delete(r.comps, name)
			log.Printf("competition %q unloaded (config file removed)", name)
		})
	}
}

// ── Competition startup ───────────────────────────────────────────────────────

func startCompetition(name string, env map[string]string) (*Competition, error) {
	token := env["BOT_TOKEN"]
	if token == "" {
		return nil, fmt.Errorf("competition %q: BOT_TOKEN not set", name)
	}
	channelIDsRaw := env["CHANNEL_ID"]
	if channelIDsRaw == "" {
		return nil, fmt.Errorf("competition %q: CHANNEL_ID not set", name)
	}
	glideURL := env["GLIDE_URL"]
	if glideURL == "" {
		return nil, fmt.Errorf("competition %q: GLIDE_URL not set", name)
	}
	bufferSize := mapInt(env, "BUFFER_SIZE", 20)
	channelIDs := strings.Split(channelIDsRaw, ",")

	hub := NewHub(bufferSize)
	go hub.Run()

	bot, err := NewBot(token, channelIDs, hub)
	if err != nil {
		return nil, fmt.Errorf("starting bot for %q: %w", name, err)
	}
	bot.PrefillBuffer(bufferSize)

	pinCounts := make(map[string]int)
	for channelID := range bot.channelIDs {
		pinCounts[channelID] = bot.FetchPinnedCount(channelID)
	}

	displayName := mapString(env, "COMP_NAME", name)
	website := env["COMP_WEBSITE"]

	comp := &Competition{
		name:        name,
		displayName: displayName,
		website:     website,
		hub:         hub,
		bot:         bot,
		glideURL:    glideURL,
		pinCounts:   pinCounts,
	}

	bot.session.AddHandler(func(s *discordgo.Session, e *discordgo.ChannelPinsUpdate) {
		if !bot.channelIDs[e.ChannelID] {
			return
		}
		newCount := bot.FetchPinnedCount(e.ChannelID)
		comp.pinMu.Lock()
		oldCount := comp.pinCounts[e.ChannelID]
		comp.pinCounts[e.ChannelID] = newCount
		comp.pinMu.Unlock()
		hub.pinsUpdated <- (newCount > oldCount)
	})

	return comp, nil
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// sseHandler streams Discord messages and pin signals to the browser.
// It tracks activeConns on the competition for idle-cleanup safety.
func sseHandler(comp *Competition) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		atomic.AddInt32(&comp.activeConns, 1)
		client := make(chan Message, 32)
		pinsSub := make(chan bool, 4)
		comp.hub.register <- client
		comp.hub.registerPins <- pinsSub
		defer func() {
			comp.hub.unregister <- client
			comp.hub.unregisterPins <- pinsSub
			atomic.AddInt32(&comp.activeConns, -1)
		}()

		for {
			select {
			case <-r.Context().Done():
				return
			case msg, ok := <-client:
				if !ok {
					return
				}
				data, err := json.Marshal(msg)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
				flusher.Flush()
			case added := <-pinsSub:
				if added {
					fmt.Fprintf(w, "event: pins-added\ndata: {}\n\n")
				} else {
					fmt.Fprintf(w, "event: pins-removed\ndata: {}\n\n")
				}
				flusher.Flush()
			}
		}
	}
}

// configHandler exposes competition metadata to the frontend.
func configHandler(glideURL, discordURL, displayName, website string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		json.NewEncoder(w).Encode(map[string]string{
			"glideUrl":    glideURL,
			"discordUrl":  discordURL,
			"compName":    displayName,
			"compWebsite": website,
		})
	}
}

// pinnedHandler returns the current pinned messages as a JSON array.
func pinnedHandler(bot *Bot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		msgs, err := bot.FetchPinnedMessages()
		if err != nil {
			http.Error(w, "failed to fetch pinned messages", http.StatusInternalServerError)
			return
		}
		if msgs == nil {
			msgs = []Message{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		json.NewEncoder(w).Encode(msgs)
	}
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	registry := &Registry{comps: make(map[string]*Competition)}

	addr := ":" + port
	log.Printf("listening on http://localhost%s", addr)
	log.Printf("add .env files to comps/ to configure competitions")
	if err := http.ListenAndServe(addr, registry); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func mapString(env map[string]string, key, fallback string) string {
	if v := env[key]; v != "" {
		return v
	}
	return fallback
}

func mapInt(env map[string]string, key string, fallback int) int {
	if v := env[key]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// ── Listing page ──────────────────────────────────────────────────────────────

const listingPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <title>CompExchange</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #0a0a14;
      color: #dcddde;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
    }
    .container { text-align: center; padding: 40px 20px; }
    h1 { font-size: 24px; font-weight: 700; color: #fff; margin-bottom: 6px; }
    .subtitle { font-size: 13px; color: #72767d; margin-bottom: 32px; }
    .comp-list { list-style: none; display: flex; flex-direction: column; gap: 10px; min-width: 280px; }
    .comp-list a {
      display: block; padding: 16px 28px;
      background: rgba(255,255,255,0.05);
      border: 1px solid rgba(255,255,255,0.08);
      border-radius: 8px;
      color: #fff; text-decoration: none;
      font-size: 15px; font-weight: 600;
      transition: background 0.15s, border-color 0.15s;
    }
    .comp-list a:hover {
      background: rgba(88,101,242,0.25);
      border-color: rgba(88,101,242,0.5);
    }
    .empty { color: #72767d; font-size: 14px; line-height: 1.6; }
    code {
      background: rgba(255,255,255,0.08);
      padding: 1px 5px; border-radius: 3px;
      font-size: 12px; color: #b9bbbe;
    }
  </style>
</head>
<body>
  <div class="container">
    <h1>CompExchange</h1>
    <p class="subtitle">Select a competition to follow</p>
    %s
  </div>
</body>
</html>`
