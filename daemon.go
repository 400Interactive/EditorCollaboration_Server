package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Background mode: -detach starts a copy of the server without console window and returns once it answers,
// -stop asks the running server to save and quit. Both find each other through a control file in the data directory.

const (
	detachedEnv     = "COLLAB_DETACHED"
	controlFileName = "server.json"
	controlPath     = "/control/shutdown"
	controlHeader   = "X-Collab-Token"
	maxLogBytes     = 10 << 20
)

type controlInfo struct {
	PID   int    `json:"pid"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

func controlFilePath(cfg config) string {
	return filepath.Join(cfg.dataDir, controlFileName)
}

// localBaseURL is how a process on this machine reaches the server.
func localBaseURL(cfg config) string {
	host, port, err := net.SplitHostPort(cfg.addr)
	if err != nil {
		host, port = "", "7878"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if cfg.tlsCert != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func localHTTPClient(timeout time.Duration) *http.Client {
	// The certificate is issued for the public host name, not for loopback.
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func isHealthy(client *http.Client, baseURL string) bool {
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func newControlToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// claimControlFile refuses to start a second server on the same data directory, then records this one.
func claimControlFile(cfg config, token string) error {
	path := controlFilePath(cfg)
	if raw, err := os.ReadFile(path); err == nil {
		var other controlInfo
		if json.Unmarshal(raw, &other) == nil && other.URL != "" && isHealthy(localHTTPClient(time.Second), other.URL) {
			return fmt.Errorf("another server (PID %d, %s) already uses the data directory %s; stop it with -stop", other.PID, other.URL, cfg.dataDir)
		}
	}
	raw, _ := json.Marshal(controlInfo{PID: os.Getpid(), URL: localBaseURL(cfg), Token: token})
	return os.WriteFile(path, raw, 0o600)
}

func releaseControlFile(cfg config, token string) {
	path := controlFilePath(cfg)
	var current controlInfo
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &current) == nil && current.Token == token {
		_ = os.Remove(path)
	}
}

// handleShutdownRequest serves -stop. Only local callers holding the token from the control file are accepted;
// requests forwarded by a reverse proxy on the same machine are refused.
func (s *server) handleShutdownRequest(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if s.controlToken == "" || r.Method != http.MethodPost || r.Header.Get("X-Forwarded-For") != "" || ip == nil || !ip.IsLoopback() {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(controlHeader)), []byte(s.controlToken)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	s.requestShutdown()
}

func (s *server) requestShutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdownRequested) })
}

// openLogFile appends to the log file, keeping one previous file once it grows past maxLogBytes.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
}

func setupLogging(cfg config) {
	if cfg.logFile == "" {
		return
	}
	file, err := openLogFile(cfg.logFile)
	if err != nil {
		log.Fatal(T("server.openLog", cfg.logFile, err))
	}
	log.SetOutput(io.MultiWriter(os.Stderr, file))
}

// runDetached starts the server in the background and waits until it answers.
func runDetached(cfg config) int {
	baseURL := localBaseURL(cfg)
	client := localHTTPClient(time.Second)
	if isHealthy(client, baseURL) {
		fmt.Fprintf(os.Stderr, "A server is already running at %s. Stop it first with -stop.\n", baseURL)
		return 1
	}

	logFile := cfg.logFile
	if logFile == "" {
		logFile = filepath.Join(cfg.dataDir, "server.log")
	}
	logFile, _ = filepath.Abs(logFile)
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot find the server executable:", err)
		return 1
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), detachedEnv+"=1", "COLLAB_LOG_FILE="+logFile)
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start the server:", err)
		return 1
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			fmt.Fprintf(os.Stderr, "The server stopped during startup. End of %s:\n%s\n", logFile, tailOf(logFile, 8))
			return 1
		case <-time.After(200 * time.Millisecond):
		}
		if isHealthy(client, baseURL) {
			fmt.Printf("Editor Collaboration server running in the background (PID %d) at %s\n", cmd.Process.Pid, baseURL)
			fmt.Printf("Logs: %s\n", logFile)
			fmt.Printf("Stop it with: %s -stop%s\n", filepath.Base(exe), dataDirArgument(cfg))
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "The server (PID %d) did not answer within 10 seconds, see %s\n", cmd.Process.Pid, logFile)
	return 1
}

func dataDirArgument(cfg config) string {
	if cfg.dataDir == "data" {
		return ""
	}
	return fmt.Sprintf(" -data %q", cfg.dataDir)
}

// runStop asks the server using this data directory to save and quit, and waits until it is gone.
func runStop(cfg config) int {
	path := controlFilePath(cfg)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("No server is running with the data directory %s.\n", cfg.dataDir)
		return 1
	}
	var info controlInfo
	if err != nil || json.Unmarshal(raw, &info) != nil || info.URL == "" {
		fmt.Fprintf(os.Stderr, "Unreadable control file %s.\n", path)
		return 1
	}

	request, _ := http.NewRequest(http.MethodPost, info.URL+controlPath, nil)
	request.Header.Set(controlHeader, info.Token)
	resp, err := localHTTPClient(3 * time.Second).Do(request)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			_ = os.Remove(path)
			fmt.Printf("No server answers at %s (PID %d was probably closed without -stop); removed %s.\n", info.URL, info.PID, path)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Cannot reach the server at %s: %v\n", info.URL, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "The server at %s refused to stop (%s).\n", info.URL, resp.Status)
		return 1
	}

	for i := 0; i < 50; i++ {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("Server stopped (PID %d).\n", info.PID)
			return 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("Stop requested; the server (PID %d) is still finishing.\n", info.PID)
	return 0
}

func tailOf(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := bytes.Split(bytes.TrimRight(raw, "\r\n"), []byte("\n"))
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return string(bytes.Join(parts, []byte("\n")))
}
