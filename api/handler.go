package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fasentneo/discovery"
	"fasentneo/transfer"
)

type Handler struct {
	discovery   *discovery.Service
	transfer    *transfer.Manager
	webFS       embed.FS
	mux         *http.ServeMux
	wsClients   map[chan []byte]struct{}
	wsMu        sync.Mutex
	uploadsDir  string
}

type wsMessage struct {
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

func NewHandler(d *discovery.Service, t *transfer.Manager, web embed.FS, uploadsDir string) *Handler {
	os.MkdirAll(uploadsDir, 0755)
	h := &Handler{
		discovery:  d,
		transfer:   t,
		webFS:      web,
		mux:        http.NewServeMux(),
		wsClients:  make(map[chan []byte]struct{}),
		uploadsDir: uploadsDir,
	}

	h.mux.HandleFunc("/api/devices", h.handleDevices)
	h.mux.HandleFunc("/api/info", h.handleInfo)
	h.mux.HandleFunc("/api/set-device-name", h.handleSetDeviceName)
	h.mux.HandleFunc("/api/downloads", h.handleDownloads)
	h.mux.HandleFunc("/api/send", h.handleSend)
	h.mux.HandleFunc("/api/open-file", h.handleOpenFile)
	h.mux.HandleFunc("/ws", h.handleWebSocket)

	// Serve embedded web files (strip "web/" prefix)
	webSub, err := fs.Sub(web, "web")
	if err != nil {
		log.Fatalf("failed to create web sub-filesystem: %v", err)
	}
	fileServer := http.FileServer(http.FS(webSub))
	h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Don't handle API routes here
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/ws" {
			http.NotFound(w, r)
			return
		}
		// Serve index.html for root path
		if r.URL.Path == "/" {
			indexFile, err := webSub.Open("index.html")
			if err != nil {
				http.Error(w, "index not found", http.StatusInternalServerError)
				return
			}
			defer indexFile.Close()
			stat, _ := indexFile.Stat()
			http.ServeContent(w, r, "index.html", stat.ModTime(), indexFile.(io.ReadSeeker))
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	go h.broadcastLoop()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		devices := h.discovery.GetDevices()
		writeJSON(w, devices)

	case "POST":
		var req struct {
			Name string `json:"name"`
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		if req.Name == "" || req.IP == "" || req.Port <= 0 {
			writeJSON(w, map[string]string{"error": "name, ip, and port are required"})
			return
		}
		id := h.discovery.AddManualDevice(req.Name, req.IP, req.Port)
		writeJSON(w, map[string]string{"id": id, "status": "added"})

	case "DELETE":
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, map[string]string{"error": "id query param required"})
			return
		}
		if h.discovery.RemoveManualDevice(id) {
			writeJSON(w, map[string]string{"status": "removed"})
		} else {
			writeJSON(w, map[string]string{"error": "device not found"})
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]interface{}{
		"id":   h.discovery.ID(),
		"name": h.discovery.Name(),
		"port": h.discovery.Port(),
		"ips":  getLocalIPs(),
	})
}

func (h *Handler) handleSetDeviceName(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, map[string]string{"error": "name is required"})
		return
	}
	h.discovery.SetName(req.Name)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries, err := os.ReadDir(h.transfer.GetDownloadDir())
	if err != nil {
		writeJSON(w, []interface{}{})
		return
	}
	type download struct {
		Name    string    `json:"name"`
		Size    int64     `json:"size"`
		ModTime time.Time `json:"mod_time"`
	}
	downloads := make([]download, 0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		downloads = append(downloads, download{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	writeJSON(w, downloads)
}

func (h *Handler) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (max 2GB)
	if err := r.ParseMultipartForm(2 << 30); err != nil {
		writeJSON(w, map[string]string{"error": "failed to parse form: " + err.Error()})
		return
	}

	targetIDsStr := r.FormValue("targetIds")
	if targetIDsStr == "" {
		writeJSON(w, map[string]string{"error": "targetIds is required"})
		return
	}
	targetIDs := strings.Split(targetIDsStr, ",")

	// Find all target devices
	devices := h.discovery.GetDevices()
	var targets []*discovery.Device
	for _, tid := range targetIDs {
		tid = strings.TrimSpace(tid)
		for _, d := range devices {
			if d.ID == tid {
				targets = append(targets, d)
				break
			}
		}
	}
	if len(targets) == 0 {
		writeJSON(w, map[string]string{"error": "no target devices found"})
		return
	}

	// Process uploaded files and send to all targets
	var results []map[string]interface{}
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fh := range fileHeaders {
			src, err := fh.Open()
			if err != nil {
				results = append(results, map[string]interface{}{
					"name":   fh.Filename,
					"status": "error",
					"error":  err.Error(),
				})
				continue
			}

			// Save to temp file (one copy shared across all targets)
			tmpPath := filepath.Join(h.uploadsDir, fh.Filename)
			dst, err := os.Create(tmpPath)
			if err != nil {
				src.Close()
				results = append(results, map[string]interface{}{
					"name":   fh.Filename,
					"status": "error",
					"error":  err.Error(),
				})
				continue
			}
			if _, err := io.Copy(dst, src); err != nil {
				dst.Close()
				src.Close()
				os.Remove(tmpPath)
				results = append(results, map[string]interface{}{
					"name":   fh.Filename,
					"status": "error",
					"error":  err.Error(),
				})
				continue
			}
			dst.Close()
			src.Close()

			// Send to each target (re-read file for each to avoid race)
			for _, target := range targets {
				go func(targetIP string, targetPort int, targetName, filePath, fileName string) {
					if _, err := h.transfer.SendFile(targetIP, targetPort, filePath); err != nil {
						log.Printf("send to %s error: %v", targetName, err)
					}
				}(target.IP, target.Port, target.Name, tmpPath, fh.Filename)
			}

			// Clean up temp file after a delay (worst case: all sends complete in background)
			go func(filePath string) {
				time.Sleep(5 * time.Minute)
				os.Remove(filePath)
			}(tmpPath)

			results = append(results, map[string]interface{}{
				"name":    fh.Filename,
				"status":  "sending",
				"targets": len(targets),
			})
		}
	}

	writeJSON(w, map[string]interface{}{
		"results":   results,
		"targetIds": targetIDs,
	})
}

func (h *Handler) handleOpenFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, map[string]string{"error": "name is required"})
		return
	}

	downloadDir := h.transfer.GetDownloadDir()
	fullPath := filepath.Join(downloadDir, filepath.Base(req.Name))

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		writeJSON(w, map[string]string{"error": "file not found: " + req.Name})
		return
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", "/select,", fullPath)
	case "darwin":
		cmd = exec.Command("open", "-R", fullPath)
	case "linux":
		// xdg-open opens the file; most file managers support selecting
		// Try opening the containing directory as fallback
		cmd = exec.Command("xdg-open", filepath.Dir(fullPath))
	default:
		writeJSON(w, map[string]string{"error": "unsupported platform"})
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("open-file error: %v", err)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, map[string]string{"status": "ok", "path": fullPath})
}

func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Simple SSE (Server-Sent Events) instead of WebSocket to avoid extra dependency
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 64)
	h.wsMu.Lock()
	h.wsClients[ch] = struct{}{}
	h.wsMu.Unlock()

	defer func() {
		h.wsMu.Lock()
		delete(h.wsClients, ch)
		h.wsMu.Unlock()
		close(ch)
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (h *Handler) broadcastLoop() {
	// Broadcast device list every 2 seconds
	deviceTicker := time.NewTicker(2 * time.Second)
	defer deviceTicker.Stop()

	// Listen for transfer progress
	progressCh := h.transfer.ProgressCh()

	for {
		select {
		case <-deviceTicker.C:
			devices := h.discovery.GetDevices()
			data, _ := json.Marshal(devices)
			msg, _ := json.Marshal(wsMessage{Type: "devices", Data: data})
			h.broadcast(msg)

		case p := <-progressCh:
			data, _ := json.Marshal(p)
			msg, _ := json.Marshal(wsMessage{Type: "progress", Data: data})
			h.broadcast(msg)
		}
	}
}

func (h *Handler) broadcast(data []byte) {
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	for ch := range h.wsClients {
		select {
		case ch <- data:
		default:
		}
	}
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func getLocalIPs() []string {
	var ips []string

	// Method 1: enumerate interfaces (works on desktop)
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}

	// Method 2: UDP dial trick (works on Android, gets primary IP)
	if len(ips) == 0 {
		conn, err := net.Dial("udp", "8.8.8.8:53")
		if err == nil {
			if udpAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok && udpAddr.IP != nil {
				ips = append(ips, udpAddr.IP.String())
			}
			conn.Close()
		}
	}

	if len(ips) == 0 {
		ips = append(ips, "127.0.0.1")
	}
	return ips
}
