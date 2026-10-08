// Command collab-server relays editor presence, chat and comments between the Unreal Editor clients of the
// Editor Collaboration plugin. Presence lives in memory only; chat history and comments are saved in the data directory.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const (
	version         = "1.2.0"
	protocolVersion = 5

	maxMessageBytes = 64 << 10
	maxRoomLength   = 128
	maxNameLength   = 64
	helloTimeout    = 10 * time.Second
	writeTimeout    = 10 * time.Second
	pingInterval    = 5 * time.Second
	pongTimeout     = 15 * time.Second
	sendQueueSize   = 256

	messagesPerSecond = 30.0
	messageBurst      = 60.0
	maxDroppedInARow  = 300
)

// Presence fields a client may share. Anything else is discarded.
var allowedFields = map[string]bool{
	"name": true, "color": true, "map": true, "cam": true,
	"asset": true, "graph": true, "nodes": true, "actors": true, "active": true,
	"pie": true, "sequencer": true, "branch": true, "actorsMore": true, "nodesMore": true,
}

type config struct {
	addr           string
	password       string
	tlsCert        string
	tlsKey         string
	maxRoomClients int
	dataDir        string
	logFile        string
	noTray         bool
}

type server struct {
	cfg          config
	passwordHash [32]byte

	mu    sync.Mutex
	rooms map[string]*room
	db    *database

	// Set only by main: tests run without the -stop endpoint.
	controlToken      string
	shutdownRequested chan struct{}
	shutdownOnce      sync.Once
}

type command int

const (
	commandRun command = iota
	commandHealthcheck
	commandDetach
	commandStop
)

// room exists while at least one client is in it; its data is loaded on the first join and dropped with it.
type room struct {
	name    string
	clients map[string]*client
	data    *roomData
}

type client struct {
	id    string
	name  string
	room  *room
	conn  *websocket.Conn
	send  chan []byte
	state map[string]json.RawMessage

	closeOnce sync.Once
	done      chan struct{}
}

type inbound struct {
	Type            string                     `json:"type"`
	ProtocolVersion int                        `json:"protocolVersion"`
	Room            string                     `json:"room"`
	Password        string                     `json:"password"`
	State           map[string]json.RawMessage `json:"state"`
	Text            string                     `json:"text"`
	ID              string                     `json:"id"`
	Anchor          json.RawMessage            `json:"anchor"`
	Resolved        *bool                      `json:"resolved"`
	Package         string                     `json:"package"`
	Asset           string                     `json:"asset"`
	Hash            string                     `json:"hash"`
	Dirty           bool                       `json:"dirty"`
	Working         bool                       `json:"working"`
	Seconds         int                        `json:"seconds"`
	Before          string                     `json:"before"`
}

type peerEntry struct {
	ID    string                     `json:"id"`
	State map[string]json.RawMessage `json:"state"`
}

func main() {
	cfg, cmd := parseConfig()
	switch cmd {
	case commandHealthcheck:
		os.Exit(runHealthcheck(cfg))
	case commandDetach:
		os.Exit(runDetached(cfg))
	case commandStop:
		os.Exit(runStop(cfg))
	}
	os.Exit(run(cfg))
}

func run(cfg config) int {
	setupLogging(cfg)
	srv := &server{
		cfg: cfg, passwordHash: sha256.Sum256([]byte(cfg.password)), rooms: map[string]*room{},
		controlToken: newControlToken(), shutdownRequested: make(chan struct{}),
	}
	if err := os.MkdirAll(cfg.dataDir, 0o750); err != nil {
		log.Print(err)
		return 1
	}
	if err := claimControlFile(cfg, srv.controlToken); err != nil {
		log.Print(err)
		return 1
	}
	defer releaseControlFile(cfg, srv.controlToken)

	db, err := openDatabase(cfg.dataDir)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer db.close()
	srv.db = db

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Printf("server error: %v", err)
		return 1
	}

	httpServer := &http.Server{
		Handler:           httpHandler(srv),
		ReadHeaderTimeout: 10 * time.Second,
	}

	scheme := "ws"
	if cfg.tlsCert != "" {
		scheme = "wss"
	}
	log.Print(T("server.started", version, protocolVersion))
	log.Print(T("server.listening", cfg.addr, scheme, portSuffix(cfg.addr)))
	log.Print(T("server.data", filepath.Join(cfg.dataDir, databaseFileName)))
	if cfg.password == "" {
		log.Print(T("server.noPassword"))
	} else if cfg.tlsCert == "" {
		log.Print(T("server.noTLS"))
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.tlsCert != "" {
			errCh <- httpServer.ServeTLS(listener, cfg.tlsCert, cfg.tlsKey)
		} else {
			errCh <- httpServer.Serve(listener)
		}
	}()

	removeTray := startTray(srv)
	defer removeTray()

	// Ctrl+C, closing the terminal window, SIGTERM from systemd or Docker, -stop and the tray menu all end up here.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-signals
		srv.requestShutdown()
	}()

	exitCode := 0
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(T("server.error", err))
			exitCode = 1
		}
	case <-srv.shutdownRequested:
		log.Print(T("server.shutdown"))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.closeAll()
		_ = httpServer.Shutdown(ctx)
	}
	db.close()
	log.Print(T("server.stopped"))
	return exitCode
}

func httpHandler(srv *server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", srv.handleWebSocket)
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc(controlPath, srv.handleShutdownRequest)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "Editor Collaboration server %s - WebSocket endpoint: /ws\n", version)
	})
	return mux
}

func parseConfig() (config, command) {
	port := envOr("COLLAB_PORT", "7878")
	host := envOr("COLLAB_HOST", "")
	maxRoom, err := strconv.Atoi(envOr("COLLAB_MAX_ROOM_CLIENTS", "64"))
	if err != nil || maxRoom <= 0 {
		maxRoom = 64
	}

	var cfg config
	flag.StringVar(&cfg.addr, "addr", net.JoinHostPort(host, port), "listen address (env COLLAB_HOST / COLLAB_PORT)")
	flag.StringVar(&cfg.password, "password", os.Getenv("COLLAB_PASSWORD"), "shared password required to join (env COLLAB_PASSWORD)")
	flag.StringVar(&cfg.tlsCert, "tls-cert", os.Getenv("COLLAB_TLS_CERT"), "TLS certificate file, enables wss:// (env COLLAB_TLS_CERT)")
	flag.StringVar(&cfg.tlsKey, "tls-key", os.Getenv("COLLAB_TLS_KEY"), "TLS private key file (env COLLAB_TLS_KEY)")
	flag.IntVar(&cfg.maxRoomClients, "max-room-clients", maxRoom, "maximum clients per room (env COLLAB_MAX_ROOM_CLIENTS)")
	flag.StringVar(&cfg.dataDir, "data", envOr("COLLAB_DATA_DIR", "data"), "directory of the database (collab.db) and of the control file (env COLLAB_DATA_DIR)")
	flag.StringVar(&cfg.logFile, "log", os.Getenv("COLLAB_LOG_FILE"), "also write the logs to this file (env COLLAB_LOG_FILE; -detach defaults to <data>/server.log)")
	flag.BoolVar(&cfg.noTray, "no-tray", envOr("COLLAB_NO_TRAY", "") == "1", "Windows: no icon in the notification area of the taskbar (env COLLAB_NO_TRAY=1)")
	healthcheck := flag.Bool("healthcheck", false, "query /health on the local server and exit (for container health checks)")
	detach := flag.Bool("detach", false, "run in the background, without console window, and return once the server answers")
	stop := flag.Bool("stop", false, "stop the server running with the same data directory, after it saved its data")
	flag.Parse()

	if (cfg.tlsCert == "") != (cfg.tlsKey == "") {
		log.Fatal(T("server.tlsBoth"))
	}
	switch {
	case *healthcheck:
		return cfg, commandHealthcheck
	case *stop:
		return cfg, commandStop
	case *detach && os.Getenv(detachedEnv) != "1":
		return cfg, commandDetach
	}
	return cfg, commandRun
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func portSuffix(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return ":" + port
	}
	return ""
}

func runHealthcheck(cfg config) int {
	if !isHealthy(localHTTPClient(3*time.Second), localBaseURL(cfg)) {
		fmt.Fprintln(os.Stderr, "no healthy server at", localBaseURL(cfg))
		return 1
	}
	return 0
}

func (s *server) counts() (clients, rooms int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rm := range s.rooms {
		clients += len(rm.clients)
	}
	return clients, len(s.rooms)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	clients, rooms := s.counts()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok", "version": version, "protocolVersion": protocolVersion,
		"rooms": rooms, "clients": clients,
	})
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Unreal clients send no browser Origin; access is controlled by the password instead.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxMessageBytes)

	c, err := s.handshake(r.Context(), conn)
	if err != nil {
		return
	}
	defer s.leave(c)

	go c.writeLoop()
	go c.pingLoop()

	log.Print(T("server.joined", c.room.name, c.name, c.id, remoteHost(r)))
	c.readLoop(r.Context(), s)
}

// handshake waits for the hello message, validates it and registers the client in its room.
func (s *server) handshake(ctx context.Context, conn *websocket.Conn) (*client, error) {
	helloCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()

	_, data, err := conn.Read(helloCtx)
	if err != nil {
		conn.CloseNow()
		return nil, err
	}

	var hello inbound
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" {
		return nil, reject(conn, "protocol", "The first message must be 'hello'.")
	}
	if hello.ProtocolVersion != protocolVersion {
		return nil, reject(conn, "version", fmt.Sprintf("Protocol version %d is not supported (server: %d). Update the plugin or the server.", hello.ProtocolVersion, protocolVersion))
	}
	given := sha256.Sum256([]byte(hello.Password))
	if subtle.ConstantTimeCompare(given[:], s.passwordHash[:]) != 1 {
		time.Sleep(500 * time.Millisecond)
		return nil, reject(conn, "auth", "Wrong password.")
	}
	roomName := strings.TrimSpace(hello.Room)
	if roomName == "" || utf8.RuneCountInString(roomName) > maxRoomLength {
		return nil, reject(conn, "room", "Invalid room name.")
	}

	state := filterState(hello.State)
	c := &client{
		id:    newID(),
		conn:  conn,
		send:  make(chan []byte, sendQueueSize),
		state: map[string]json.RawMessage{},
		done:  make(chan struct{}),
	}
	for key, value := range state {
		if !isNull(value) {
			c.state[key] = value
		}
	}
	c.name = displayName(c.state)

	s.mu.Lock()
	rm := s.rooms[roomName]
	if rm == nil {
		// The database is read without the server lock; another client may create the room meanwhile.
		s.mu.Unlock()
		loaded, err := s.db.loadRoom(roomName)
		if err != nil {
			log.Print(T("server.loadRoom", roomName, err))
			return nil, reject(conn, "storage", "The server cannot read its database.")
		}
		s.mu.Lock()
		if rm = s.rooms[roomName]; rm == nil {
			rm = &room{name: roomName, clients: map[string]*client{}, data: loaded}
			s.rooms[roomName] = rm
		}
	}
	saved := rm.data
	if len(rm.clients) >= s.cfg.maxRoomClients {
		if len(rm.clients) == 0 {
			delete(s.rooms, roomName)
		}
		s.mu.Unlock()
		return nil, reject(conn, "full", "The room is full.")
	}
	peers := make([]peerEntry, 0, len(rm.clients))
	for _, other := range rm.clients {
		peers = append(peers, peerEntry{ID: other.id, State: copyState(other.state)})
	}
	// Queued while holding the lock so that no presence update can reach this client before its roster.
	c.enqueue(mustJSON(map[string]any{"type": "welcome", "selfId": c.id, "protocolVersion": protocolVersion}))
	c.enqueue(mustJSON(map[string]any{"type": "roster", "peers": peers}))
	c.enqueue(mustJSON(map[string]any{"type": "chatHistory", "messages": saved.Chat, "complete": saved.ChatComplete}))
	c.enqueue(mustJSON(map[string]any{"type": "comments", "comments": saved.Comments}))
	c.enqueue(mustJSON(map[string]any{"type": "assets", "records": saved.Assets}))
	c.room = rm
	rm.clients[c.id] = c
	s.broadcastLocked(rm, c.id, mustJSON(map[string]any{"type": "presence", "id": c.id, "state": c.state}))
	s.mu.Unlock()

	return c, nil
}

func (c *client) readLoop(ctx context.Context, s *server) {
	tokens := messageBurst
	last := time.Now()
	dropped := 0

	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}

		now := time.Now()
		tokens = min(messageBurst, tokens+now.Sub(last).Seconds()*messagesPerSecond)
		last = now
		if tokens < 1 {
			dropped++
			if dropped > maxDroppedInARow {
				c.conn.Close(websocket.StatusPolicyViolation, "too many messages")
				return
			}
			continue
		}
		tokens--
		dropped = 0

		var msg inbound
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "presence":
			s.updatePresence(c, filterState(msg.State))
		case "chat", "comment.create", "comment.reply", "comment.resolve", "comment.move", "comment.delete", "asset.modify", "asset.clear", "ping":
			s.handleCollab(c, msg)
		case "chat.history":
			s.sendOlderChat(c, msg.Before)
		}
	}
}

// sendOlderChat answers the client scrolling up its chat. Reads the database without the server lock.
func (s *server) sendOlderChat(c *client, before string) {
	s.mu.Lock()
	roomName := ""
	if c.room != nil {
		roomName = c.room.name
	}
	s.mu.Unlock()
	if roomName == "" || before == "" {
		return
	}
	s.db.flush()
	messages, complete, err := s.db.chatPage(roomName, before)
	if err != nil {
		log.Print(T("server.readChat", roomName, err))
		c.enqueue(errorMessage("storage", "", "The server cannot read its database."))
		return
	}
	c.enqueue(mustJSON(map[string]any{"type": "chatOlder", "before": before, "messages": messages, "complete": complete}))
}

func (s *server) updatePresence(c *client, delta map[string]json.RawMessage) {
	if len(delta) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if c.room == nil {
		return
	}
	for key, value := range delta {
		if isNull(value) {
			delete(c.state, key)
		} else {
			c.state[key] = value
		}
	}
	if _, ok := delta["name"]; ok {
		c.name = displayName(c.state)
	}
	s.broadcastLocked(c.room, c.id, mustJSON(map[string]any{"type": "presence", "id": c.id, "state": delta}))
}

func (s *server) leave(c *client) {
	s.mu.Lock()
	rm := c.room
	if rm != nil {
		if rm.data != nil {
			for _, record := range rm.data.clearWorking(c.id) {
				s.broadcastLocked(rm, c.id, mustJSON(map[string]any{"type": "assetModified", "record": record}))
			}
		}
		delete(rm.clients, c.id)
		if len(rm.clients) == 0 {
			delete(s.rooms, rm.name)
		} else {
			s.broadcastLocked(rm, c.id, mustJSON(map[string]any{"type": "leave", "id": c.id}))
		}
		c.room = nil
	}
	s.mu.Unlock()

	c.shutdown(websocket.StatusNormalClosure, "")
	if rm != nil {
		log.Print(T("server.left", rm.name, c.name, c.id))
	}
}

// broadcastLocked queues a message for every client of the room except the sender. Requires s.mu.
func (s *server) broadcastLocked(rm *room, senderID string, message []byte) {
	for id, other := range rm.clients {
		if id != senderID {
			other.enqueue(message)
		}
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	var all []*client
	for _, rm := range s.rooms {
		for _, c := range rm.clients {
			all = append(all, c)
		}
	}
	s.mu.Unlock()
	for _, c := range all {
		c.shutdown(websocket.StatusGoingAway, "server shutting down")
	}
}

func (c *client) enqueue(message []byte) {
	select {
	case <-c.done:
	case c.send <- message:
	default:
		go c.shutdown(websocket.StatusPolicyViolation, "client too slow")
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case message := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, message)
			cancel()
			if err != nil {
				c.shutdown(websocket.StatusInternalError, "")
				return
			}
		}
	}
}

func (c *client) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), pongTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				c.shutdown(websocket.StatusPolicyViolation, "no ping response")
				return
			}
		}
	}
}

func (c *client) shutdown(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		go func() {
			if err := c.conn.Close(code, reason); err != nil {
				c.conn.CloseNow()
			}
		}()
	})
}

func reject(conn *websocket.Conn, code, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"type": "error", "code": code, "message": message}))
	_ = conn.Close(websocket.StatusPolicyViolation, code)
	return errors.New(code)
}

func filterState(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		if !allowedFields[key] || len(value) == 0 {
			continue
		}
		if key == "name" || key == "color" {
			var text string
			if json.Unmarshal(value, &text) != nil || utf8.RuneCountInString(text) > maxNameLength {
				continue
			}
		}
		out[key] = value
	}
	return out
}

func displayName(state map[string]json.RawMessage) string {
	var name string
	if raw, ok := state["name"]; ok && json.Unmarshal(raw, &name) == nil && strings.TrimSpace(name) != "" {
		return strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, name)
	}
	return "?"
}

func isNull(value json.RawMessage) bool {
	return strings.TrimSpace(string(value)) == "null"
}

func copyState(state map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(state))
	for key, value := range state {
		out[key] = value
	}
	return out
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func remoteHost(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
