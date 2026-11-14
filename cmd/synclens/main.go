package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"synclens/internal/auth"
	"synclens/internal/config"
	"synclens/internal/discovery"
	"synclens/internal/transport"
	"synclens/internal/web"
)

func main() {
	peerMode := flag.Bool("peer", false, "Run in Peer Mode (for the remote device)")
	guiMode := flag.Bool("gui", false, "Run in Dashboard GUI Mode")
	pinFlag := flag.String("pin", "", "Optional 6-digit pairing PIN for Peer Mode")
	portFlag := flag.Int("port", 0, "Optional override port")
	flag.Parse()

	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "Windows-PC"
	}

	if !*peerMode && !*guiMode {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("=== SyncLens — LAN Visual Diff & Sync ===")
		fmt.Println("  [1] Dashboard GUI (Perangkat Utama — periksa & bandingkan file)")
		fmt.Println("  [2] Peer Mode     (Perangkat Lawan — tampilkan IP & PIN)")
		fmt.Print("Pilih mode [1]: ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if choice == "2" {
			*peerMode = true
		} else {
			*guiMode = true
		}
	}

	if *peerMode {
		runPeer(host, *pinFlag, *portFlag)
		return
	}
	runDashboard(host, *portFlag)
}

func runPeer(deviceName, customPIN string, overridePort int) {
	pin := customPIN
	if pin == "" {
		var err error
		pin, err = auth.GeneratePIN()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating PIN: %v\n", err)
			os.Exit(1)
		}
	}

	httpPort := config.DefaultPeerPort
	if overridePort > 0 {
		httpPort = overridePort
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := discovery.StartResponder(ctx, config.DefaultDiscoveryPort, httpPort, deviceName, pin); err != nil {
			fmt.Fprintf(os.Stderr, "UDP discovery warning: %v\n", err)
		}
	}()

	srv := transport.NewPeerServer(deviceName, pin, runtime.NumCPU())
	httpSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", httpPort),
		Handler: srv.Handler(),
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Println("\n=== SyncLens PEER MODE Aktif ===")
	fmt.Printf("Nama Perangkat : %s\n", deviceName)
	fmt.Printf("Pairing PIN    : %s\n", pin)
	fmt.Printf("Port LAN       : TCP :%d | UDP :%d\n", httpPort, config.DefaultDiscoveryPort)
	fmt.Printf("IP LAN Lokal   : %s\n", strings.Join(localIPv4s(), ", "))
	fmt.Println("\nBiarkan jendela ini terbuka selama proses perbandingan/sinkronisasi.")
	fmt.Println("Tutup jendela ini atau tekan Ctrl+C untuk berhenti.")

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func runDashboard(deviceName string, overridePort int) {
	webPort := config.DefaultWebPort
	if overridePort > 0 {
		webPort = overridePort
	}

	dash, err := web.NewDashboardServer(deviceName, config.DefaultPairsFilePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init dashboard: %v\n", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", webPort)
	url := "http://" + addr

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: dash.Handler(),
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Printf("\n=== SyncLens Dashboard GUI ===\n")
	fmt.Printf("Membuka browser di: %s\n", url)
	fmt.Println("Tutup jendela ini atau tekan Ctrl+C untuk keluar.")
	openBrowser(url)

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", err)
		os.Exit(1)
	}
}

func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

func localIPv4s() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if ip4 := ipNet.IP.To4(); ip4 != nil {
					ips = append(ips, ip4.String())
				}
			}
		}
	}
	if len(ips) == 0 {
		ips = append(ips, "127.0.0.1")
	}
	return ips
}
