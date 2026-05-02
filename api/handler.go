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
	"path/filepath"
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

type sendRequest struct {
	TargetID string `json:"targetId"`
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
	h.mux.HandleFunc("/api/downloads", h.handleDownloads)
	h.mux.HandleFunc("/api/send", h.handleSend)
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

	targetID := r.FormValue("targetId")
	if targetID == "" {
		writeJSON(w, map[string]string{"error": "targetId is required"})
		return
	}

	// Find target device
	devices := h.discovery.GetDevices()
	var target *discovery.Device
	for _, d := range devices {
		if d.ID == targetID {
			target = d
			break
		}
	}
	if target == nil {
		writeJSON(w, map[string]string{"error": "target device not found"})
		return
	}

	// Process uploaded files
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

			// Save to temp file
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

			// Send file to target
			go func(targetIP string, targetPort int, filePath, fileName string) {
				_, err := h.transfer.SendFile(targetIP, targetPort, filePath)
				if err != nil {
					log.Printf("send error: %v", err)
				}
				// Clean up temp file
				os.Remove(filePath)
			}(target.IP, target.Port, tmpPath, fh.Filename)

			results = append(results, map[string]interface{}{
				"name":   fh.Filename,
				"status": "sending",
			})
		}
	}

	writeJSON(w, map[string]interface{}{"results": results})
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
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return []string{"unknown"}
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			ips = append(ips, ipNet.IP.String())
		}
	}
	if len(ips) == 0 {
		ips = append(ips, "127.0.0.1")
	}
	return ips
}
