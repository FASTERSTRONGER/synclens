package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"synclens/internal/auth"
)

type Beacon struct {
	Type       string `json:"type"` // "DISCOVER" or "HERE"
	DeviceName string `json:"device_name"`
	HTTPPort   int    `json:"http_port"`
	Timestamp  int64  `json:"timestamp"`
	HMAC       string `json:"hmac"`
}

type PeerInfo struct {
	DeviceName string `json:"device_name"`
	IP         string `json:"ip"`
	HTTPPort   int    `json:"http_port"`
}

func signBeacon(b *Beacon, pin string) {
	payload := fmt.Sprintf("%s|%s|%d|%d", b.Type, b.DeviceName, b.HTTPPort, b.Timestamp)
	b.HMAC = auth.ComputeHMAC(pin, payload)
}

func verifyBeacon(b Beacon, pin string, now time.Time) bool {
	diff := now.Unix() - b.Timestamp
	if diff < -auth.MaxSkewSeconds || diff > auth.MaxSkewSeconds {
		return false
	}
	payload := fmt.Sprintf("%s|%s|%d|%d", b.Type, b.DeviceName, b.HTTPPort, b.Timestamp)
	return b.HMAC != "" && b.HMAC == auth.ComputeHMAC(pin, payload)
}

// StartResponder listens for authenticated UDP discovery beacons and replies with peer connection info.
func StartResponder(ctx context.Context, udpPort, httpPort int, deviceName, pin string) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: udpPort, IP: net.IPv4zero})
	if err != nil {
		return fmt.Errorf("listen udp :%d: %w", udpPort, err)
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 4096)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		var req Beacon
		if err := json.Unmarshal(buf[:n], &req); err != nil {
			continue
		}
		if req.Type != "DISCOVER" || !verifyBeacon(req, pin, time.Now()) {
			continue
		}
		resp := Beacon{
			Type:       "HERE",
			DeviceName: deviceName,
			HTTPPort:   httpPort,
			Timestamp:  time.Now().Unix(),
		}
		signBeacon(&resp, pin)
		data, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(data, remoteAddr)
	}
}

// DiscoverPeer broadcasts on all active IPv4 interfaces and returns the first valid peer response.
func DiscoverPeer(ctx context.Context, udpPort int, pin, selfDevice string, timeout time.Duration) (*PeerInfo, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0, IP: net.IPv4zero})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := Beacon{
		Type:       "DISCOVER",
		DeviceName: selfDevice,
		Timestamp:  time.Now().Unix(),
	}
	signBeacon(&req, pin)
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	for _, dst := range broadcastTargets(udpPort) {
		_, _ = conn.WriteToUDP(payload, dst)
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("tidak ada peer ditemukan di LAN dalam %s (cek PIN atau masukkan IP manual)", timeout)
		}
		var resp Beacon
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			continue
		}
		if resp.Type != "HERE" || !verifyBeacon(resp, pin, time.Now()) {
			continue
		}
		return &PeerInfo{
			DeviceName: resp.DeviceName,
			IP:         remoteAddr.IP.String(),
			HTTPPort:   resp.HTTPPort,
		}, nil
	}
}

func broadcastTargets(port int) []*net.UDPAddr {
	seen := map[string]bool{}
	var addrs []*net.UDPAddr
	add := func(ip net.IP) {
		key := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		if !seen[key] {
			seen[key] = true
			addrs = append(addrs, &net.UDPAddr{IP: ip, Port: port})
		}
	}
	add(net.IPv4bcast)

	ifaces, err := net.Interfaces()
	if err != nil {
		return addrs
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		ifAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range ifAddrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || len(ipNet.Mask) != 4 {
				continue
			}
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip4[i] | ^ipNet.Mask[i]
			}
			add(bcast)
		}
	}
	return addrs
}
