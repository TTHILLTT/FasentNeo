package discovery

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	broadcastPort     = 54321
	broadcastInterval = 3 * time.Second
	deviceTimeout     = 10 * time.Second
)

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	LastSeen time.Time `json:"-"`
	IsLocal  bool      `json:"is_local"`
	Manual   bool      `json:"manual"`
}

type Message struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

type Service struct {
	id            string
	name          string
	port          int
	mu            sync.RWMutex
	devices       map[string]*Device
	manualDevices map[string]*Device
	stopCh        chan struct{}
	conn          *net.UDPConn
}

func New(name string, transferPort int) *Service {
	return &Service{
		id:            generateID(),
		name:          name,
		port:          transferPort,
		devices:       make(map[string]*Device),
		manualDevices: make(map[string]*Device),
		stopCh:        make(chan struct{}),
	}
}

func (s *Service) Start() error {
	var err error
	s.conn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: broadcastPort})
	if err != nil {
		return fmt.Errorf("failed to listen on UDP %d: %w", broadcastPort, err)
	}

	s.conn.SetReadBuffer(65536)
	s.conn.SetWriteBuffer(65536)

	go s.broadcastLoop()
	go s.listenLoop()

	return nil
}

func (s *Service) Stop() {
	close(s.stopCh)
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Service) GetDevices() []*Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	result := make([]*Device, 0)

	// LAN discovered devices
	for id, d := range s.devices {
		if now.Sub(d.LastSeen) > deviceTimeout {
			continue
		}
		if id == s.id {
			continue
		}
		result = append(result, d)
	}

	// Manual/remote devices (never expire)
	for _, d := range s.manualDevices {
		result = append(result, d)
	}

	return result
}

func (s *Service) ID() string       { return s.id }
func (s *Service) Port() int     { return s.port }
func (s *Service) Name() string  { return s.name }

func (s *Service) AddManualDevice(name, ip string, port int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := generateID()
	s.manualDevices[id] = &Device{
		ID:       id,
		Name:     name,
		IP:       ip,
		Port:     port,
		LastSeen: time.Now(),
		Manual:   true,
	}
	return id
}

func (s *Service) RemoveManualDevice(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.manualDevices[id]
	if ok {
		delete(s.manualDevices, id)
	}
	return ok
}

func (s *Service) broadcastLoop() {
	ticker := time.NewTicker(broadcastInterval)
	defer ticker.Stop()

	msg := Message{
		Type: "announce",
		ID:   s.id,
		Name: s.name,
		Port: s.port,
	}
	data, _ := json.Marshal(msg)

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.broadcast(data)
		}
	}
}

func (s *Service) broadcast(data []byte) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return
	}

	sent := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			bcast := make(net.IP, len(ipNet.IP.To4()))
			copy(bcast, ipNet.IP.To4())
			mask := ipNet.Mask
			for i := range bcast {
				bcast[i] |= ^mask[i]
			}
			bcastStr := bcast.String()
			if sent[bcastStr] {
				continue
			}
			sent[bcastStr] = true

			dst := &net.UDPAddr{IP: bcast, Port: broadcastPort}
			s.conn.WriteToUDP(data, dst)
		}
	}

	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: broadcastPort}
	if !sent[dst.IP.String()] {
		s.conn.WriteToUDP(data, dst)
	}
}

func (s *Service) listenLoop() {
	buf := make([]byte, 4096)
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		s.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, remoteAddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return
			default:
				continue
			}
		}

		var msg Message
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}

		if msg.Type != "announce" || msg.ID == s.id {
			continue
		}

		s.mu.Lock()
		s.devices[msg.ID] = &Device{
			ID:       msg.ID,
			Name:     msg.Name,
			IP:       remoteAddr.IP.String(),
			Port:     msg.Port,
			LastSeen: time.Now(),
		}
		s.mu.Unlock()
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
