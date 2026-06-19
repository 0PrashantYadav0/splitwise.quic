package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// Config controls how the QUIC/HTTP3 server is brought up.
type Config struct {
	Addr              string   // host:port, e.g. ":4433"
	Hosts             []string // SANs for the self-signed cert
	RequireClientCert bool     // turn on mutual TLS (the "complex" knob)
}

// Server bundles the TCP (h1/h2) and UDP (h3/QUIC) listeners plus WebTransport.
type Server struct {
	cfg  Config
	cert *CertBundle
	wt   *webtransport.Server
	tcp  *http.Server
	udp  *net.UDPConn
}

// CertHashB64 exposes the base64 SHA-256 cert digest for WebTransport pinning.
func (s *Server) CertHashB64() string { return s.cert.SHA256B64 }

// WebTransport returns the underlying WT server so handlers can Upgrade().
func (s *Server) WebTransport() *webtransport.Server { return s.wt }

// New builds the server, generating a fresh short-lived ECDSA cert.
// buildHandler receives the *Server (so the WT upgrade handler can reach it)
// and returns the fully-wired http.Handler mounted on every listener.
func New(cfg Config, buildHandler func(*Server) http.Handler) (*Server, error) {
	cert, err := GenerateCert(cfg.Hosts)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, cert: cert}

	// QUIC tuning — this is where the "complex techniques" live.
	quicConf := &quic.Config{
		// 0-RTT: returning clients can send data in the very first flight,
		// shaving a full round-trip off reconnects.
		Allow0RTT: true,
		// Unreliable QUIC DATAGRAM frames (RFC 9221) power our live balance pushes.
		EnableDatagrams: true,
		// Generous stream limits so a single connection can multiplex many
		// concurrent expense/feed requests without head-of-line blocking.
		MaxIncomingStreams:    512,
		MaxIncomingUniStreams: 512,
		// Keep idle QUIC connections warm to make connection migration
		// (Wi-Fi <-> cellular handoff) seamless; quic-go validates the new path.
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  2 * time.Minute,
	}

	tlsConf := cert.TLSConfig(cfg.RequireClientCert)

	h3 := &http3.Server{
		Addr:            cfg.Addr,
		TLSConfig:       tlsConf,
		QUICConfig:      quicConf,
		EnableDatagrams: true,
	}
	s.wt = &webtransport.Server{H3: h3}

	// Precompute the Alt-Svc value so we can advertise HTTP/3 on the very
	// first TCP response, free of the listener-registration race in quic-go.
	port := portOf(cfg.Addr)
	altSvc := fmt.Sprintf(`h3=":%d"; ma=2592000`, port)

	// Build the application handler (closes over s for WT upgrades).
	handler := buildHandler(s)
	s.wt.H3.Handler = withAltSvc(handler, altSvc)

	// TCP listener (HTTP/1.1 + HTTP/2) — browsers bootstrap here, then the
	// Alt-Svc header tells them to upgrade to h3 over UDP.
	s.tcp = &http.Server{
		Addr:      cfg.Addr,
		Handler:   withAltSvc(handler, altSvc),
		TLSConfig: tlsConf,
	}
	return s, nil
}

// portOf extracts the numeric port from a "host:port" address (default 4433).
func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 4433
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 4433
	}
	return n
}

// withAltSvc advertises HTTP/3 availability on every response so compliant
// clients seamlessly upgrade from TCP to QUIC on subsequent requests.
func withAltSvc(next http.Handler, altSvc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", altSvc)
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe starts both listeners and blocks until one errors.
func (s *Server) ListenAndServe() error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("resolve udp: %w", err)
	}
	s.udp, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("HTTP/3 (QUIC) listening on udp %s", s.cfg.Addr)
		errCh <- s.wt.Serve(s.udp)
	}()
	go func() {
		log.Printf("HTTP/2 (TCP/TLS) listening on tcp %s", s.cfg.Addr)
		errCh <- s.tcp.ListenAndServeTLS("", "")
	}()
	return <-errCh
}

// Shutdown gracefully tears down both listeners.
func (s *Server) Shutdown(ctx context.Context) error {
	_ = s.wt.Close()
	if s.udp != nil {
		_ = s.udp.Close()
	}
	return s.tcp.Shutdown(ctx)
}
