package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const shutdownSocket = "/run/cba-shutdown.sock"

func sendShutdown(ctx context.Context, socket string) error {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("Closing shutdown socket: %v", err)
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(conn, "shutdown"); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if reply != "accepted\n" {
		return fmt.Errorf("shutdown rejected: %q", reply)
	}
	return nil
}

func shutdownHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := sendShutdown(r.Context(), shutdownSocket); err != nil {
		log.Printf("Shutdown not accepted: %v", err)
		http.Error(w, "host shutdown request failed", http.StatusServiceUnavailable)
		return
	}
	if _, err := fmt.Fprintln(w, "Host accepted shutdown"); err != nil {
		log.Printf("Writing shutdown acknowledgement: %v", err)
	}
}

func findMainInterfaceAndMAC() (string, string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", "", fmt.Errorf("reading route table: %w", err)
	}

	var mainIface string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "00000000" {
			mainIface = fields[0]
			break
		}
	}

	if mainIface == "" {
		return "", "", fmt.Errorf("could not determine main interface from /proc/net/route")
	}

	iface, err := net.InterfaceByName(mainIface)
	if err != nil {
		return "", "", fmt.Errorf("getting interface %s: %w", mainIface, err)
	}

	if iface.HardwareAddr == nil {
		return "", "", fmt.Errorf("interface %s has no MAC address", mainIface)
	}

	return mainIface, iface.HardwareAddr.String(), nil
}

func macHandler(w http.ResponseWriter, r *http.Request) {
	iface, mac, err := findMainInterfaceAndMAC()
	if err != nil {
		http.Error(w, "error: "+err.Error(), http.StatusInternalServerError)
		log.Println("[/mac] Failed:", err)
		return
	}

	resp := map[string]string{
		"interface": iface,
		"mac":       mac,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func main() {
	http.HandleFunc("/shutdown", shutdownHandler)
	http.HandleFunc("/mac", macHandler)
	log.Println("Listening on :9101 for requests")
	if err := http.ListenAndServe(":9101", nil); err != nil {
		log.Fatalf("ListenAndServe failed: %v", err)
	}
}
