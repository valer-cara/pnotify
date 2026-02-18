// process_watcher — watches for new processes matching configurable criteria
// and sends desktop notifications via D-Bus (org.freedesktop.Notifications).
//
// Build:
//
//	go build -o notifier .
//
// Usage:
//
//	./notifier                          # uses ./config.json
//	./notifier -config criteria.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/godbus/dbus/v5"
	"github.com/shirou/gopsutil/v3/process"
)

const fallbackPollInterval = 2 * time.Second

// ---------------------------------------------------------------------------
// Config JSON structures
// ---------------------------------------------------------------------------

type criterionRaw struct {
	Name        string        `json:"name"`
	Match       criterionMatch `json:"match"`
	NotifyTitle string        `json:"notify_title"`
	NotifyBody  string        `json:"notify_body"`
	Urgency     string        `json:"urgency"`
}

type criterionMatch struct {
	NameRegex       string   `json:"name_regex"`
	CmdlineContains []string `json:"cmdline_contains"`
	Username        string   `json:"username"`
}

// ---------------------------------------------------------------------------
// Criterion
// ---------------------------------------------------------------------------

type Criterion struct {
	Name            string
	nameRegex       *regexp.Regexp
	cmdlineContains []string
	username        string
	notifyTitle     string
	notifyBody      string
	urgency         string
}

func buildCriteria(raw []criterionRaw) ([]*Criterion, error) {
	out := make([]*Criterion, 0, len(raw))
	for _, r := range raw {
		c := &Criterion{
			Name:            r.Name,
			cmdlineContains: r.Match.CmdlineContains,
			username:        r.Match.Username,
			notifyTitle:     r.NotifyTitle,
			notifyBody:      r.NotifyBody,
			urgency:         r.Urgency,
		}
		if c.urgency == "" {
			c.urgency = "normal"
		}
		if c.notifyTitle == "" {
			c.notifyTitle = "New process"
		}
		if c.notifyBody == "" {
			c.notifyBody = "PID {pid}: {name}"
		}
		if r.Match.NameRegex != "" {
			re, err := regexp.Compile("(?i)" + r.Match.NameRegex)
			if err != nil {
				return nil, fmt.Errorf("criterion %q: invalid name_regex: %w", r.Name, err)
			}
			c.nameRegex = re
		}
		out = append(out, c)
	}
	return out, nil
}

// tmplVar matches Python-style {key} placeholders.
var tmplVar = regexp.MustCompile(`\{(\w+)\}`)

func formatTemplate(tmpl string, ctx map[string]string) string {
	return tmplVar.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := ctx[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

func (c *Criterion) matches(proc *process.Process) bool {
	name, err := proc.Name()
	if err != nil {
		return false
	}
	parts, _ := proc.CmdlineSlice()
	cmdline := strings.Join(parts, " ")
	username, _ := proc.Username()

	if c.nameRegex != nil && !c.nameRegex.MatchString(name) {
		return false
	}
	for _, term := range c.cmdlineContains {
		if !strings.Contains(cmdline, term) {
			return false
		}
	}
	if c.username != "" && username != c.username {
		return false
	}
	return true
}

func (c *Criterion) formatNotification(proc *process.Process) (string, string) {
	name, _ := proc.Name()
	parts, _ := proc.CmdlineSlice()
	username, _ := proc.Username()
	ctx := map[string]string{
		"name":     name,
		"pid":      fmt.Sprintf("%d", proc.Pid),
		"cmdline":  strings.Join(parts, " "),
		"username": username,
	}
	return formatTemplate(c.notifyTitle, ctx), formatTemplate(c.notifyBody, ctx)
}

// ---------------------------------------------------------------------------
// Notifications (D-Bus)
// ---------------------------------------------------------------------------

func sendNotification(title, body, urgency string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("D-Bus session: %w", err)
	}
	urgencyMap := map[string]byte{"low": 0, "normal": 1, "critical": 2}
	u, ok := urgencyMap[urgency]
	if !ok {
		u = 1
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(u)}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call(
		"org.freedesktop.Notifications.Notify", 0,
		"process_watcher", uint32(0), "", title, body, []string{}, hints, int32(-1),
	).Err
}

// ---------------------------------------------------------------------------
// ProcessWatcher
// ---------------------------------------------------------------------------

type ProcessWatcher struct {
	configPath string
	mu         sync.RWMutex
	criteria   []*Criterion
}

func newProcessWatcher(configPath string, criteria []*Criterion) *ProcessWatcher {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = configPath
	}
	return &ProcessWatcher{
		configPath: abs,
		criteria:   criteria,
	}
}

func (w *ProcessWatcher) reloadConfig() {
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		log.Printf("Config reload failed: %v", err)
		return
	}
	var raw []criterionRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("Config reload failed, keeping existing criteria: %v", err)
		return
	}
	criteria, err := buildCriteria(raw)
	if err != nil {
		log.Printf("Config reload failed, keeping existing criteria: %v", err)
		return
	}
	w.mu.Lock()
	w.criteria = criteria
	w.mu.Unlock()
	log.Printf("Config reloaded: %d criteria from %s", len(criteria), w.configPath)
}

func (w *ProcessWatcher) snapshot() map[int32]struct{} {
	pids, _ := process.Pids()
	m := make(map[int32]struct{}, len(pids))
	for _, pid := range pids {
		m[pid] = struct{}{}
	}
	return m
}

func (w *ProcessWatcher) checkNew(newPIDs map[int32]struct{}) {
	w.mu.RLock()
	criteria := make([]*Criterion, len(w.criteria))
	copy(criteria, w.criteria)
	w.mu.RUnlock()

	for pid := range newPIDs {
		proc, err := process.NewProcess(pid)
		if err != nil {
			continue // process already gone
		}
		for _, c := range criteria {
			if c.matches(proc) {
				title, body := c.formatNotification(proc)
				log.Printf("MATCH [%s] — %s | %s", c.Name, title, body)
				if err := sendNotification(title, body, c.urgency); err != nil {
					log.Printf("Notification error: %v", err)
				}
			}
		}
	}
}

func (w *ProcessWatcher) run() {
	w.mu.RLock()
	names := make([]string, len(w.criteria))
	for i, c := range w.criteria {
		names[i] = c.Name
	}
	w.mu.RUnlock()
	log.Printf("Watching %d criteria: %v", len(names), names)

	// Config file hot-reload via fsnotify.
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Warning: could not create file watcher: %v", err)
	} else {
		defer fw.Close()
		if err := fw.Add(filepath.Dir(w.configPath)); err != nil {
			log.Printf("Warning: could not watch config dir: %v", err)
		} else {
			go func() {
				for {
					select {
					case event, ok := <-fw.Events:
						if !ok {
							return
						}
						if filepath.Clean(event.Name) == w.configPath &&
							(event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
							w.reloadConfig()
						}
					case err, ok := <-fw.Errors:
						if !ok {
							return
						}
						log.Printf("File watcher error: %v", err)
					}
				}
			}()
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pidCh, err := listenProcExec(ctx)
	if err != nil {
		log.Printf("Warning: netlink CN_PROC unavailable (%v), falling back to polling every %v", err, fallbackPollInterval)
		w.runPolling(sigCh)
		return
	}
	log.Printf("Process watcher started in netlink CN_PROC mode.")

	for {
		select {
		case pid, ok := <-pidCh:
			if !ok {
				log.Printf("Netlink channel closed, falling back to polling every %v", fallbackPollInterval)
				w.runPolling(sigCh)
				return
			}
			w.checkNew(map[int32]struct{}{pid: {}})
		case s := <-sigCh:
			log.Printf("Received signal %v, stopping.", s)
			return
		}
	}
}

func (w *ProcessWatcher) runPolling(sigCh chan os.Signal) {
	log.Printf("Process watcher polling every %v.", fallbackPollInterval)
	seen := w.snapshot()

	ticker := time.NewTicker(fallbackPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			current := w.snapshot()
			newPIDs := make(map[int32]struct{})
			for pid := range current {
				if _, ok := seen[pid]; !ok {
					newPIDs[pid] = struct{}{}
				}
			}
			if len(newPIDs) > 0 {
				w.checkNew(newPIDs)
			}
			seen = current
		case s := <-sigCh:
			log.Printf("Received signal %v, stopping.", s)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	configFile := flag.String("config", "", "Path to JSON config file (default: ./config.json)")
	flag.Parse()

	log.SetFlags(log.Ltime)

	configPath := *configFile
	if configPath == "" {
		configPath = "config.json"
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Config file not found: %s", configPath)
	}
	var raw []criterionRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}
	criteria, err := buildCriteria(raw)
	if err != nil {
		log.Fatalf("Invalid config: %v", err)
	}

	watcher := newProcessWatcher(configPath, criteria)
	watcher.run()
}
