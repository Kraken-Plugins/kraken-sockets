package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	PacketJoin      = "JOIN"
	PacketLeave     = "LEAVE"
	PacketBroadcast = "BROADCAST"
	PacketMessage   = "MESSAGE"
)

const (
	writeWait      = 10 * time.Second
	readTimeout    = 45 * time.Second
	pingPeriod     = 30 * time.Second
	maxMessageSize = 64 * 1024
)

var websocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Client represents a connected websocket client.
type Client struct {
	conn          *websocket.Conn
	name          string
	encryptedName string
	room          string
	once          sync.Once
	writeMutex    sync.Mutex
	lastActive    int64
	done          chan struct{}
}

// Room represents a group of connected clients.
type Room struct {
	id      string
	clients map[string]*Client
	mutex   sync.RWMutex
}

type SocketServer struct {
	rooms map[string]*Room
	mutex sync.RWMutex
}

// Get or create a room.
func (s *SocketServer) getRoom(id string) *Room {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	room, exists := s.rooms[id]
	if !exists {
		room = &Room{
			id:      id,
			clients: make(map[string]*Client),
		}
		s.rooms[id] = room
	}

	return room
}

// Add a client to a room.
func (r *Room) addClient(client *Client) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.clients[client.encryptedName] = client
}

// Remove a client from a room.
func (r *Room) removeClient(client *Client) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	delete(r.clients, client.encryptedName)
}

// Get all clients in a room.
func (r *Room) getAllClients() []*Client {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	clients := make([]*Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}

	return clients
}

// Handle a websocket client connection.
func handleClient(w http.ResponseWriter, r *http.Request, server *SocketServer) {
	conn, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("failed to upgrade websocket connection: %v", err)
		return
	}

	client := &Client{
		conn:       conn,
		lastActive: time.Now().UnixNano(),
		done:       make(chan struct{}),
	}

	conn.SetReadLimit(maxMessageSize)
	err = conn.SetReadDeadline(time.Now().Add(readTimeout))
	if err != nil {
		log.Errorf("failed to set websocket read deadline: %v", err)
		_ = conn.Close()
		return
	}

	conn.SetPongHandler(func(string) error {
		atomic.StoreInt64(&client.lastActive, time.Now().UnixNano())
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	room, err := initializeClient(client, server)
	if err != nil {
		log.Errorf("failed to initialize websocket client: %v", err)
		_ = conn.Close()
		return
	}

	go processClientMessages(client, room)
	go monitorConnection(client, room)
}

func initializeClient(client *Client, server *SocketServer) (*Room, error) {
	_, payload, err := client.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to read join packet: %w", err)
	}

	atomic.StoreInt64(&client.lastActive, time.Now().UnixNano())

	packet, err := decodePacket(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to parse join packet: %w", err)
	}

	header, ok := packet["header"].(string)
	if !ok || header != PacketJoin {
		return nil, fmt.Errorf("invalid join packet header")
	}

	roomID, ok := packet["room"].(string)
	if !ok || roomID == "" {
		return nil, fmt.Errorf("missing room ID")
	}

	encryptedName, ok := packet["name"].(string)
	if !ok || encryptedName == "" {
		return nil, fmt.Errorf("missing player name")
	}

	log.Infof("JOIN packet header: %s, room: %s, name: %s", header, roomID, encryptedName)

	room := server.getRoom(roomID)

	client.room = roomID
	client.encryptedName = encryptedName
	client.name = DecryptAES(roomID, encryptedName)

	log.Infof("player %s joined room %s", client.name, roomID)
	room.addClient(client)
	notifyJoin(room, client)

	return room, nil
}

func decodePacket(payload []byte) (map[string]interface{}, error) {
	trimmedPayload := strings.TrimSpace(string(payload))
	if trimmedPayload == "" {
		return nil, fmt.Errorf("empty packet")
	}

	packet := make(map[string]interface{})
	if err := json.Unmarshal([]byte(trimmedPayload), &packet); err != nil {
		return nil, err
	}

	return packet, nil
}

func (c *Client) writeText(payload []byte) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}

	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Client) writeJSON(packet map[string]interface{}) error {
	jsonData, err := json.Marshal(packet)
	if err != nil {
		return err
	}

	return c.writeText(jsonData)
}

func (c *Client) sendPing() error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	deadline := time.Now().Add(writeWait)
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	return c.conn.WriteControl(websocket.PingMessage, nil, deadline)
}

// Send a join notification to all clients in a room.
func notifyJoin(room *Room, joiningClient *Client) {
	clients := room.getAllClients()

	memberNames := make([]string, 0, len(clients))
	for _, client := range clients {
		memberNames = append(memberNames, client.encryptedName)
	}

	joinPacket := map[string]interface{}{
		"header": PacketJoin,
		"player": joiningClient.encryptedName,
		"party":  memberNames,
	}

	log.Infof("notifying client join to all parties")
	for _, client := range clients {
		if err := client.writeJSON(joinPacket); err != nil {
			log.Errorf("error writing join packet for client %s: %v", client.name, err)
		}
	}
}

// Send a leave notification to all clients in a room.
func notifyLeave(room *Room, leavingClient *Client) {
	room.removeClient(leavingClient)

	clients := room.getAllClients()

	memberNames := make([]string, 0, len(clients))
	for _, client := range clients {
		memberNames = append(memberNames, client.encryptedName)
	}

	leavePacket := map[string]interface{}{
		"header": PacketLeave,
		"player": leavingClient.encryptedName,
		"party":  memberNames,
	}

	for _, client := range clients {
		if err := client.writeJSON(leavePacket); err != nil {
			log.Errorf("error writing leave packet for client %s: %v", client.name, err)
		}
	}
}

// Process messages from a client.
// This function owns client.done and closes it on exit so that
// monitorConnection stops at the same time.
func processClientMessages(client *Client, room *Room) {
	defer close(client.done)

	for {
		messageType, payload, err := client.conn.ReadMessage()
		if err != nil {
			log.Errorf("client %s disconnected: %v", client.name, err)
			client.once.Do(func() { notifyLeave(room, client) })
			return
		}

		if messageType != websocket.TextMessage {
			continue
		}

		atomic.StoreInt64(&client.lastActive, time.Now().UnixNano())
		err = client.conn.SetReadDeadline(time.Now().Add(readTimeout))
		if err != nil {
			log.Errorf("failed to refresh websocket read deadline: %v", err)
		}

		trimmedPayload := strings.TrimSpace(string(payload))
		if trimmedPayload == "" {
			continue
		}

		packet, err := decodePacket(payload)
		if err != nil {
			log.Errorf("failed to parse socket client packet: %v", err)
			continue
		}

		header, ok := packet["header"].(string)
		if !ok {
			log.Errorf("missing packet header")
			continue
		}

		switch header {
		case PacketBroadcast:
			clients := room.getAllClients()
			for _, roomClient := range clients {
				if err := roomClient.writeText([]byte(trimmedPayload)); err != nil {
					log.Errorf("error writing broadcast packet for client %s: %v", roomClient.name, err)
				}
			}
		case PacketLeave:
			log.Infof("client %s sent graceful LEAVE", client.name)
			client.once.Do(func() {
				notifyLeave(room, client)
			})
			_ = client.conn.Close()
			return
		default:
			log.Infof("unknown packet header: %s", header)
		}
	}
}

// monitorConnection watches for idle clients. It exits as soon as
// processClientMessages closes client.done, preventing spurious
// timeout log lines for clients that already disconnected cleanly.
func monitorConnection(client *Client, room *Room) {
	ticker := time.NewTicker(10 * time.Second)
	pingTicker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer pingTicker.Stop()

	for {
		select {
		case <-client.done:
			return
		case <-ticker.C:
			lastActive := time.Unix(0, atomic.LoadInt64(&client.lastActive))
			if time.Since(lastActive) > readTimeout {
				log.Infof("websocket client %s timed out", client.name)
				client.once.Do(func() {
					notifyLeave(room, client)
				})
				_ = client.conn.Close()
				return
			}
		case <-pingTicker.C:
			if err := client.sendPing(); err != nil {
				log.Errorf("failed to send websocket ping to client %s: %v", client.name, err)
				client.once.Do(func() {
					notifyLeave(room, client)
				})
				_ = client.conn.Close()
				return
			}
		}
	}
}

// Listen Registers a new socket server listening on the defined host and port.
func Listen(host, port string) {
	server := &SocketServer{
		rooms: make(map[string]*Room),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}

		handleClient(w, r, server)
	})

	address := fmt.Sprintf("%s:%s", host, port)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Infof("websocket server started on %s", address)
	err := httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("failed to start websocket server on %s err: %v", address, err)
	}
}
