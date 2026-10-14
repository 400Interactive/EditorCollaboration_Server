// Command collab-server relays editor presence, chat and comments between the Unreal Editor clients of the
// Editor Collaboration plugin. Presence lives in memory only; chat history and comments are saved in the data directory.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
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
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const (
	version         = "1.3.0"
	protocolVersion = 6

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

	// A presence value larger than this is dropped. The whole frame is already limited by maxMessageBytes.
	maxPresenceFieldBytes = 8 << 10
	maxConnections        = 256
	maxRooms              = 64
)

// Presence fields a client may share. Anything else is discarded.
var allowedFields = map[string]bool{
	"name": true, "color": true, "map": true, "cam": true,
	"asset": true, "graph": true, "nodes": true, "actors": true, "active": true,
	"pie": true, "play": true, "sequencer": true, "branch": true, "actorsMore": true, "nodesMore": true,
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
	autoUpdate     bool
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

	updateChecked   bool
	updateAvailable bool
	latestVersion   string
	releasePage     string
	announced       string
	restartOnExit   bool
	autoUpdate      bool

	updateMu   sync.Mutex
	updateBusy bool

	conns atomic.Int32
}

type command int

const (
	commandRun command = iota
	commandHealthcheck
	commandDetach
	commandStop
	commandCheckUpdate
	commandApplyUpdate
)

// room exists while at least one client is in it; its data is loaded on the first join and dropped with it.
type room struct {
	name    string
	clients map[string]*client
	data    *roomData
}

// online tells whether an editor with this author id is in the room. Requires server.mu.
func (rm *room) online(authorID string) bool {
	for _, other := range rm.clients {
		if other.author == authorID {
			return true
		}
	}
	return false
}

type client struct {
	id   string
	name string
	// author stamps chat, comments and modified-file records. It is derived from the secret client id the editor
	// keeps across launches, so a reconnection or a restart keeps it, and the others cannot reuse it.
	author string
	// session is secret and changes at each editor launch.
	session string
	room    *room
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
	MessageID       string                     `json:"messageId"`
	Anchor          json.RawMessage            `json:"anchor"`
	Resolved        *bool                      `json:"resolved"`
	Package         string                     `json:"package"`
	Asset           string                     `json:"asset"`
	Hash            string                     `json:"hash"`
	Dirty           bool                       `json:"dirty"`
	Working         bool                       `json:"working"`
	Seconds         int                        `json:"seconds"`
	Before          string                     `json:"before"`
	ClientID        string                     `json:"clientId"`
	Session         string                     `json:"session"`
}

type peerEntry struct {
	ID       string                     `json:"id"`
	AuthorID string                     `json:"authorId"`
	State    map[string]json.RawMessage `json:"state"`
}

func main() {
	loadPluginDescriptor()
	cfg, cmd := parseConfig()
	switch cmd {
	case commandHealthcheck:
		os.Exit(runHealthcheck(cfg))
	case commandDetach:
		os.Exit(runDetached(cfg))
	case commandStop:
		os.Exit(runStop(cfg))
	case commandCheckUpdate:
		os.Exit(runCheckUpdate())
	case commandApplyUpdate:
		os.Exit(runApplyUpdate())
	}
	os.Exit(run(cfg))
}

func run(cfg config) int {
	setupLogging(cfg)
	srv := &server{
		cfg: cfg, passwordHash: sha256.Sum256([]byte(cfg.password)), rooms: map[string]*room{},
		controlToken: newControlToken(), shutdownRequested: make(chan struct{}),
		autoUpdate: cfg.autoUpdate,
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

	if cfg.password == "" && !listenHostIsLoopback(cfg.addr) {
		log.Print(T("server.passwordRequired"))
		return 1
	}

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Printf("server error: %v", err)
		return 1
	}

	// ReadTimeout and WriteTimeout stay unset: they survive Hijack and would drop live WebSockets.
	// Short routes get their own deadlines in limitRequest. Accept also clears any leftover deadline.
	httpServer := &http.Server{
		Handler:           httpHandler(srv),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
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
	if updatesEnabled() {
		go srv.watchUpdates()
	}

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
	srv.mu.Lock()
	restart := srv.restartOnExit
	srv.mu.Unlock()
	if restart && exitCode == 0 {
		reexec()
	}
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
	return limitRequest(mux)
}

// limitRequest bounds ordinary HTTP requests. WebSocket upgrades are excluded: a read deadline
// left on the hijacked connection would close the session.
func limitRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			deadline := time.Now().Add(15 * time.Second)
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(deadline)
			_ = rc.SetWriteDeadline(deadline)
		}
		next.ServeHTTP(w, r)
	})
}

// clearDeadlines strips the deadlines http.Server may have set, once the WebSocket hijacks the connection.
type clearDeadlines struct {
	http.ResponseWriter
	http.Hijacker
}

func (c clearDeadlines) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := c.Hijacker.Hijack()
	if conn != nil {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, rw, err
}

// listenHostIsLoopback reports whether addr accepts connections only from this machine.
// An empty host (":7878") listens on every interface.
func listenHostIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.Trim(addr, "[]")
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) tryAcquireConn() bool {
	for {
		current := s.conns.Load()
		if current >= maxConnections {
			return false
		}
		if s.conns.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (s *server) releaseConn() {
	s.conns.Add(-1)
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
	flag.BoolVar(&cfg.autoUpdate, "auto-update", envTruthy("COLLAB_AUTO_UPDATE"), "install a newer GitHub release when one is found (env COLLAB_AUTO_UPDATE=1)")
	healthcheck := flag.Bool("healthcheck", false, "query /health on the local server and exit (for container health checks)")
	detach := flag.Bool("detach", false, "run in the background, without console window, and return once the server answers")
	stop := flag.Bool("stop", false, "stop the server running with the same data directory, after it saved its data")
	checkUpdate := flag.Bool("check-update", false, "print whether a newer GitHub release exists, then exit")
	applyUpdate := flag.Bool("update", false, "download the latest GitHub release and replace this executable")
	flag.Parse()

	if (cfg.tlsCert == "") != (cfg.tlsKey == "") {
		log.Fatal(T("server.tlsBoth"))
	}
	switch {
	case *checkUpdate:
		return cfg, commandCheckUpdate
	case *applyUpdate:
		return cfg, commandApplyUpdate
	case *healthcheck:
		return cfg, commandHealthcheck
	case *stop:
		return cfg, commandStop
	case *detach && os.Getenv(detachedEnv) != "1":
		return cfg, commandDetach
	}
	return cfg, commandRun
}

func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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

	s.mu.Lock()
	checked, available, latest, page := s.updateChecked, s.updateAvailable, s.latestVersion, s.releasePage
	s.mu.Unlock()

	payload := map[string]any{
		"status": "ok", "version": version, "protocolVersion": protocolVersion,
		"rooms": rooms, "clients": clients,
	}
	if pluginVersion != "" {
		payload["pluginVersion"] = pluginVersion
	}
	if pluginMarketplaceURL != "" {
		payload["marketplaceUrl"] = pluginMarketplaceURL
	}
	if checked {
		payload["updateAvailable"] = available
		if latest != "" {
			payload["latestVersion"] = latest
		}
		if page != "" {
			payload["releaseUrl"] = page
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.tryAcquireConn() {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer s.releaseConn()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return
	}
	conn, err := websocket.Accept(clearDeadlines{ResponseWriter: w, Hijacker: hj}, r, &websocket.AcceptOptions{
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
	if !validClientKey(hello.ClientID) || !validClientKey(hello.Session) {
		return nil, reject(conn, "protocol", "The hello message needs a clientId and a session.")
	}

	state := filterState(hello.State)
	c := &client{
		id:      newID(),
		author:  authorID(hello.ClientID),
		session: hello.Session,
		conn:    conn,
		send:    make(chan []byte, sendQueueSize),
		state:   map[string]json.RawMessage{},
		done:    make(chan struct{}),
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
			if len(s.rooms) >= maxRooms {
				s.mu.Unlock()
				return nil, reject(conn, "full", "The server is hosting too many rooms.")
			}
			rm = &room{name: roomName, clients: map[string]*client{}, data: loaded}
			s.rooms[roomName] = rm
		}
	}
	saved := rm.data
	var stale *client
	for _, other := range rm.clients {
		if other.author == c.author && other.session == c.session {
			stale = other
			break
		}
	}
	others := len(rm.clients)
	if stale != nil {
		others--
	}
	if others >= s.cfg.maxRoomClients {
		if len(rm.clients) == 0 {
			delete(s.rooms, roomName)
		}
		s.mu.Unlock()
		return nil, reject(conn, "full", "The room is full.")
	}
	var joined []delivery
	staleName, staleID := "", ""
	if stale != nil {
		staleName, staleID = stale.name, stale.id
		s.replaceStale(rm, stale, &joined)
	}
	peers := make([]peerEntry, 0, len(rm.clients))
	for _, other := range rm.clients {
		peers = append(peers, peerEntry{ID: other.id, AuthorID: other.author, State: copyState(other.state)})
	}
	// Queued while holding the lock so that no presence update can reach this client before its roster.
	c.enqueue(mustJSON(map[string]any{"type": "welcome", "selfId": c.id, "authorId": c.author, "protocolVersion": protocolVersion}))
	c.enqueue(mustJSON(map[string]any{"type": "roster", "peers": peers}))
	c.enqueue(mustJSON(map[string]any{"type": "chatHistory", "messages": saved.Chat, "complete": saved.ChatComplete}))
	c.enqueue(mustJSON(map[string]any{"type": "comments", "comments": saved.Comments}))
	c.enqueue(mustJSON(map[string]any{"type": "assets", "records": saved.Assets}))
	c.room = rm
	rm.clients[c.id] = c
	queueBroadcast(rm, c.id, map[string]any{"type": "presence", "id": c.id, "authorId": c.author, "state": c.state}, &joined)
	deliver(joined)
	writes := saved.takePending()
	s.mu.Unlock()
	s.persist(writes)
	if staleID != "" {
		log.Print(T("server.left", roomName, staleName, staleID))
	}

	return c, nil
}

// replaceStale drops a connection the same editor session left behind (its socket is not closed yet) in favor of
// the new one. Its records stay, since both have the same author id. Requires s.mu.
func (s *server) replaceStale(rm *room, stale *client, batch *[]delivery) {
	delete(rm.clients, stale.id)
	stale.room = nil
	queueBroadcast(rm, stale.id, map[string]any{"type": "leave", "id": stale.id}, batch)
	stale.shutdown(websocket.StatusGoingAway, "replaced by a new connection")
}

func (c *client) readLoop(ctx context.Context, s *server) {
	tokens := messageBurst
	last := time.Now()

	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}

		// Past the limit, reading pauses instead of dropping: clients send only changes, so a lost one stays wrong.
		now := time.Now()
		tokens = min(messageBurst, tokens+now.Sub(last).Seconds()*messagesPerSecond)
		last = now
		if tokens < 1 {
			select {
			case <-time.After(time.Duration((1 - tokens) / messagesPerSecond * float64(time.Second))):
			case <-c.done:
				return
			case <-ctx.Done():
				return
			}
			tokens = 1
			last = time.Now()
		}
		tokens--

		var msg inbound
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "presence":
			s.updatePresence(c, filterState(msg.State))
		case "chat", "comment.create", "comment.reply", "comment.resolve", "comment.move", "comment.edit", "comment.delete", "asset.modify", "asset.clear", "ping":
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
	var batch []delivery
	var writes []func(*sql.Tx) error
	if rm := c.room; rm != nil {
		for key, value := range delta {
			if isNull(value) {
				delete(c.state, key)
			} else {
				c.state[key] = value
			}
		}
		renamed := false
		if _, ok := delta["name"]; ok {
			previous := c.name
			c.name = displayName(c.state)
			renamed = c.name != previous
		}
		queueBroadcast(rm, c.id, map[string]any{"type": "presence", "id": c.id, "state": delta}, &batch)
		if renamed && rm.data != nil {
			for _, change := range rm.data.renameAuthor(c.author, c.name, rm.online) {
				queueBroadcast(rm, "", map[string]any{"type": "assetCleared", "package": change.record.Package, "author": change.previousName}, &batch)
				queueBroadcast(rm, "", map[string]any{"type": "assetModified", "record": change.record}, &batch)
			}
			writes = rm.data.takePending()
		}
	}
	deliver(batch)
	s.mu.Unlock()
	s.persist(writes)
}

func (s *server) leave(c *client) {
	s.mu.Lock()
	var batch []delivery
	var writes []func(*sql.Tx) error
	rm := c.room
	if rm != nil {
		delete(rm.clients, c.id)
		// Another editor launched from the same project copy has the same author id and keeps the marks.
		if rm.data != nil && !rm.online(c.author) {
			for _, record := range rm.data.clearWorking(c.author) {
				queueBroadcast(rm, c.id, map[string]any{"type": "assetModified", "record": record}, &batch)
			}
			writes = rm.data.takePending()
		}
		if len(rm.clients) == 0 {
			delete(s.rooms, rm.name)
		} else {
			queueBroadcast(rm, c.id, map[string]any{"type": "leave", "id": c.id}, &batch)
		}
		c.room = nil
	}
	deliver(batch)
	s.mu.Unlock()
	s.persist(writes)

	c.shutdown(websocket.StatusNormalClosure, "")
	if rm != nil {
		log.Print(T("server.left", rm.name, c.name, c.id))
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
		if !allowedFields[key] || len(value) == 0 || len(value) > maxPresenceFieldBytes {
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

// authorID is the public id of a secret client id: everyone in the room sees it, nobody can send it back.
func authorID(clientID string) string {
	sum := sha256.Sum256([]byte("author:" + clientID))
	return hex.EncodeToString(sum[:16])
}

func validClientKey(key string) bool {
	if len(key) < 16 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
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
