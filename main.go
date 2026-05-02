package main

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"fasentneo/api"
	"fasentneo/discovery"
	"fasentneo/transfer"
)

//go:embed web
var webFiles embed.FS

var version = "dev"

const (
	httpPort     = 8080
	transferPort = 54322
)

func main() {
	hostname, _ := os.Hostname()

	// Set up directories
	homeDir, _ := os.UserHomeDir()
	downloadDir := filepath.Join(homeDir, "Downloads", "FasentNeo")
	uploadsDir := filepath.Join(os.TempDir(), "FasentNeoUploads")

	log.SetFlags(log.Ltime)
	log.Printf("FasentNeo v%s - Fast File Transfer", version)
	log.Printf("Device: %s", hostname)

	// Start discovery
	d := discovery.New(hostname, transferPort)
	if err := d.Start(); err != nil {
		log.Fatalf("Discovery failed: %v", err)
	}
	defer d.Stop()
	log.Println("Device discovery started")

	// Start transfer receiver
	tm := transfer.NewManager(downloadDir, transferPort)
	if err := tm.StartReceive(); err != nil {
		log.Fatalf("Transfer receiver failed: %v", err)
	}
	defer tm.Stop()
	log.Printf("File receiver started on port %d", transferPort)

	// Get local IP for display
	localIPs := getLocalIPs()
	for _, ip := range localIPs {
		log.Printf("Local address: %s:%d", ip, transferPort)
	}

	// Start HTTP server
	handler := api.NewHandler(d, tm, webFiles, uploadsDir)
	url := fmt.Sprintf("http://localhost:%d", httpPort)

	// Open browser
	go func() {
		openBrowser(url)
	}()

	log.Printf("Web UI available at %s", url)
	if len(localIPs) > 0 {
		log.Printf("Access from other devices: http://%s:%d", localIPs[0], httpPort)
	}
	log.Printf("Downloads saved to: %s", downloadDir)
	log.Println("Press Ctrl+C to exit")

	if err := http.ListenAndServe(fmt.Sprintf(":%d", httpPort), handler); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
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

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	}
	if err != nil {
		log.Printf("Could not open browser: %v", err)
		log.Printf("Please open %s manually", url)
	}
}
