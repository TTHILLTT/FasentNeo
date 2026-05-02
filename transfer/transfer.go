package transfer

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func (m *Manager) GetDownloadDir() string { return m.downloadDir }

// Buffer size optimized for high-speed LAN transfers
const transferBufferSize = 1024 * 1024 // 1MB

type Status string

const (
	StatusTransferring Status = "transferring"
	StatusComplete     Status = "complete"
	StatusError        Status = "error"
)

type Progress struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Sent      int64   `json:"sent"`
	Direction string  `json:"direction"`
	Status    Status  `json:"status"`
	Error     string  `json:"error,omitempty"`
	File      string  `json:"file,omitempty"`
}

type sendHeader struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type responseMsg struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type Manager struct {
	downloadDir string
	receivePort int
	listener    net.Listener
	progressCh  chan Progress
	transfers   map[string]*Progress
	mu          sync.RWMutex
	stopCh      chan struct{}
}

func NewManager(downloadDir string, receivePort int) *Manager {
	os.MkdirAll(downloadDir, 0755)
	return &Manager{
		downloadDir: downloadDir,
		receivePort: receivePort,
		progressCh:  make(chan Progress, 100),
		transfers:   make(map[string]*Progress),
		stopCh:      make(chan struct{}),
	}
}

func (m *Manager) ProgressCh() <-chan Progress { return m.progressCh }

func (m *Manager) StartReceive() error {
	addr := fmt.Sprintf(":%d", m.receivePort)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start receiver: %w", err)
	}
	m.listener = l
	go m.acceptLoop()
	return nil
}

func (m *Manager) Stop() {
	close(m.stopCh)
	if m.listener != nil {
		m.listener.Close()
	}
}

func (m *Manager) acceptLoop() {
	for {
		select {
		case <-m.stopCh:
			return
		default:
		}

		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.stopCh:
				return
			default:
				continue
			}
		}
		go m.handleReceive(conn)
	}
}

func (m *Manager) handleReceive(conn net.Conn) {
	defer conn.Close()

	tcpConn, ok := conn.(*net.TCPConn)
	if ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(transferBufferSize)
		tcpConn.SetWriteBuffer(transferBufferSize)
	}

	// Read header (JSON line)
	decoder := json.NewDecoder(conn)
	var header sendHeader
	if err := decoder.Decode(&header); err != nil {
		return
	}

	if header.Type != "send" {
		return
	}

	// Accept the transfer
	accept := responseMsg{Type: "accept", ID: header.ID}
	respData, _ := json.Marshal(accept)
	respData = append(respData, '\n')
	conn.Write(respData)

	// Create file in download directory
	destPath := filepath.Join(m.downloadDir, header.Name)
	// Avoid overwriting: add suffix if file exists
	destPath = uniquePath(destPath)

	f, err := os.Create(destPath)
	if err != nil {
		m.reportProgress(header.ID, header.Name, header.Size, 0, "receive", StatusError, err.Error())
		return
	}
	defer f.Close()

	m.reportProgress(header.ID, header.Name, header.Size, 0, "receive", StatusTransferring, "")

	// Read file data
	buf := make([]byte, transferBufferSize)
	var received int64
	for received < header.Size {
		n, err := conn.Read(buf)
		if n > 0 {
			_, writeErr := f.Write(buf[:n])
			if writeErr != nil {
				m.reportProgress(header.ID, header.Name, header.Size, received, "receive", StatusError, writeErr.Error())
				return
			}
			received += int64(n)
			m.reportProgress(header.ID, header.Name, header.Size, received, "receive", StatusTransferring, "")
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			m.reportProgress(header.ID, header.Name, header.Size, received, "receive", StatusError, err.Error())
			return
		}
	}

	// Read the done message
	var done responseMsg
	decoder = json.NewDecoder(conn)
	if err := decoder.Decode(&done); err != nil {
		// Even without done message, the file might be complete
	}

	finish := responseMsg{Type: "complete", ID: header.ID}
	finishData, _ := json.Marshal(finish)
	finishData = append(finishData, '\n')
	conn.Write(finishData)

	m.reportProgress(header.ID, header.Name, header.Size, received, "receive", StatusComplete, "")
}

func (m *Manager) SendFile(targetIP string, targetPort int, filePath string) (Progress, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return Progress{}, fmt.Errorf("cannot access file: %w", err)
	}

	id := generateID()
	name := filepath.Base(filePath)
	size := stat.Size()

	addr := net.JoinHostPort(targetIP, fmt.Sprintf("%d", targetPort))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return Progress{}, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}
	defer conn.Close()

	tcpConn, ok := conn.(*net.TCPConn)
	if ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(transferBufferSize)
		tcpConn.SetWriteBuffer(transferBufferSize)
	}

	// Send header
	header := sendHeader{Type: "send", ID: id, Name: name, Size: size}
	headerData, _ := json.Marshal(header)
	headerData = append(headerData, '\n')
	if _, err := conn.Write(headerData); err != nil {
		return Progress{}, fmt.Errorf("failed to send header: %w", err)
	}

	// Read response
	decoder := json.NewDecoder(conn)
	var resp responseMsg
	if err := decoder.Decode(&resp); err != nil {
		return Progress{}, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.Type == "reject" {
		return Progress{}, fmt.Errorf("transfer rejected: %s", resp.Reason)
	}

	m.reportProgress(id, name, size, 0, "send", StatusTransferring, "")

	// Send file data
	f, err := os.Open(filePath)
	if err != nil {
		return Progress{}, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	buf := make([]byte, transferBufferSize)
	var sent int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, writeErr := conn.Write(buf[:n]); writeErr != nil {
				m.reportProgress(id, name, size, sent, "send", StatusError, writeErr.Error())
				return Progress{}, fmt.Errorf("failed to send data: %w", writeErr)
			}
			sent += int64(n)
			m.reportProgress(id, name, size, sent, "send", StatusTransferring, "")
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			m.reportProgress(id, name, size, sent, "send", StatusError, readErr.Error())
			return Progress{}, fmt.Errorf("failed to read file: %w", readErr)
		}
	}

	// Send done
	done := responseMsg{Type: "done", ID: id}
	doneData, _ := json.Marshal(done)
	doneData = append(doneData, '\n')
	conn.Write(doneData)

	// Read completion
	var finish responseMsg
	decoder = json.NewDecoder(conn)
	decoder.Decode(&finish)

	m.reportProgress(id, name, size, sent, "send", StatusComplete, "")
	return Progress{
		ID:        id,
		Name:      name,
		Size:      size,
		Sent:      sent,
		Direction: "send",
		Status:    StatusComplete,
	}, nil
}

func (m *Manager) reportProgress(id, name string, size, sent int64, direction string, status Status, errStr string) {
	p := Progress{
		ID:        id,
		Name:      name,
		Size:      size,
		Sent:      sent,
		Direction: direction,
		Status:    status,
		Error:     errStr,
	}
	select {
	case m.progressCh <- p:
	default:
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := path[:len(path)-len(ext)]
	for i := 1; i < 1000; i++ {
		newPath := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			return newPath
		}
	}
	return fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext)
}
