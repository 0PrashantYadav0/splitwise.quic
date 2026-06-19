// Command h3check is a tiny HTTP/3 (QUIC) client used to verify that the
// Splitwise-QUIC server is reachable over QUIC. It deliberately skips cert
// verification because the dev server uses a self-signed certificate.
//
//	go run ./cmd/h3check https://localhost:4433/login
package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/quic-go/quic-go/http3"
)

func main() {
	url := "https://localhost:4433/login"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev cert
	}
	defer tr.Close()

	client := &http.Client{Transport: tr}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("HTTP/3 request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("OK over %s -> %s (%d bytes)\n", resp.Proto, resp.Status, len(body))
}
