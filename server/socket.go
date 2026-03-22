package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	PasswordSalt = "9#jx[VHk_<44nK$%0PbOTCcJA6Jy(o"
)

const (
	PacketJoin      = "JOIN"
	PacketLeave     = "LEAVE"
	PacketBroadcast = "BROADCAST"
	PacketMessage   = "MESSAGE"
)

// Client represents a connected socket client
type Client struct {
	conn          net.Conn
	name          string
	encryptedName string
	room          string
	once          sync.Once
	// lastActive stores Unix nanoseconds and is accessed atomically
	// to prevent a data race between processClientMessages (writer)
	// and monitorConnection (reader).
	lastActive int64
	writer     *bufio.Writer
	reader     *bufio.Reader
	// done is closed by processClientMessages when it exits (for any
	// reason). monitorConnection selects on it so it stops immediately
	// rather than continuing to poll a client that is already gone.
	done chan struct{}
}

// Room represents a group of connected clients
type Room struct {
	id      string
	clients map[string]*Client
	mutex   sync.RWMutex
}

type SocketServer struct {
	rooms map[string]*Room
	mutex sync.RWMutex
}

func NewSocketServer() *SocketServer {
	return &SocketServer{
		rooms: make(map[string]*Room),
	}
}

// Get or create a room
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

// Add a client to a room
func (r *Room) addClient(client *Client) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.clients[client.encryptedName] = client
}

// Remove a client from a room
func (r *Room) removeClient(client *Client) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	delete(r.clients, client.encryptedName)
}

// Get all clients in a room
func (r *Room) getAllClients() []*Client {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	clients := make([]*Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}

	return clients
}

// Handle a client connection
func handleClient(conn net.Conn, server *SocketServer) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	client := &Client{
		conn:       conn,
		lastActive: time.Now().UnixNano(),
		writer:     writer,
		reader:     reader,
		done:       make(chan struct{}),
	}

	// Wait for initial join packet
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Errorf("failed to read join packet: %v", err)
		return
	}

	// Parse the join packet
	var joinPacket map[string]interface{}
	if err := json.Unmarshal([]byte(line), &joinPacket); err != nil {
		log.Errorf("failed to parse join packet: %v", err)
		return
	}

	// Validate the join packet
	header, ok := joinPacket["header"].(string)
	if !ok || header != PacketJoin {
		log.Errorf("invalid join packet header")
		return
	}

	roomID, ok := joinPacket["room"].(string)
	if !ok {
		log.Errorf("missing room ID")
		return
	}

	encryptedName, ok := joinPacket["name"].(string)
	if !ok {
		log.Errorf("missing player name")
		return
	}
	log.Infof("JOIN packet header: %s, room: %s, name: %s", header, roomID, encryptedName)

	// Get the room
	room := server.getRoom(roomID)

	// Set up the client
	client.room = roomID
	client.encryptedName = encryptedName

	client.name = DecryptAES(roomID, encryptedName)

	log.Infof("player %s joined room %s", client.name, roomID)
	room.addClient(client)
	notifyJoin(room, client)

	// processClientMessages owns the done channel and closes it on exit.
	// monitorConnection selects on done so it exits at the same time.
	go processClientMessages(client, room)
	go monitorConnection(client, room)
}

// Send a join notification to all clients in a room
func notifyJoin(room *Room, joiningClient *Client) {
	clients := room.getAllClients()

	// Create a JSON array of all member names
	memberNames := make([]string, 0, len(clients))
	for _, client := range clients {
		memberNames = append(memberNames, client.encryptedName)
	}

	// Create the join packet
	joinPacket := map[string]interface{}{
		"header": PacketJoin,
		"player": joiningClient.encryptedName,
		"party":  memberNames,
	}

	// Convert to JSON and send to all clients
	jsonData, err := json.Marshal(joinPacket)
	if err != nil {
		log.Println("Error creating join packet:", err)
		return
	}

	jsonString := string(jsonData) + "\n"

	log.Infof("notifying client join to all parties")
	for _, client := range clients {
		_, err := client.writer.WriteString(jsonString)
		if err != nil {
			log.Errorf("error writing join packet for client: %s, %v", client.name, err)
		}
		err = client.writer.Flush()
		if err != nil {
			log.Errorf("error flushing join packet for client: %s, %v", client.name, err)
		}
	}
}

// Send a leave notification to all clients in a room
func notifyLeave(room *Room, leavingClient *Client) {
	// Remove the client from the room first
	room.removeClient(leavingClient)

	// Get remaining clients
	clients := room.getAllClients()

	// Create a JSON array of remaining member names
	memberNames := make([]string, 0, len(clients))
	for _, client := range clients {
		memberNames = append(memberNames, client.encryptedName)
	}

	// Create the leave packet
	leavePacket := map[string]interface{}{
		"header": PacketLeave,
		"player": leavingClient.encryptedName,
		"party":  memberNames,
	}

	// Convert to JSON and send to all clients
	jsonData, err := json.Marshal(leavePacket)
	if err != nil {
		log.Errorf("error creating leave packet: %v", err)
		return
	}

	jsonString := string(jsonData) + "\n"

	for _, client := range clients {
		_, err := client.writer.WriteString(jsonString)
		if err != nil {
			log.Errorf("error writing join packet for client: %s, %v", client.name, err)
		}
		err = client.writer.Flush()
		if err != nil {
			log.Errorf("error flushing join packet for client: %s, %v", client.name, err)
		}
	}
}

// Process messages from a client.
// This function owns client.done and closes it on exit so that
// monitorConnection stops at the same time.
func processClientMessages(client *Client, room *Room) {
	// Always signal the monitor to stop when this goroutine exits,
	// regardless of whether it was a graceful leave, a read error,
	// or a deadline timeout.
	defer close(client.done)

	for {
		conn := client.conn
		reader := client.reader

		// Set a read deadline. If no data (including a heartbeat) arrives
		// within 40 s the connection is considered dead and we exit.
		// The client sends a heartbeat every 30 s so 40 s gives a comfortable
		// margin without waiting as long as the monitor's 45 s window.
		err := conn.SetReadDeadline(time.Now().Add(40 * time.Second))
		if err != nil {
			log.Errorf("failed to set read deadline: %v", err)
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			log.Errorf("client %s disconnected: %v", client.name, err)
			client.once.Do(func() { notifyLeave(room, client) })
			return
		}

		// Update last-active atomically so monitorConnection always sees
		// a consistent value without a data race on the time.Time struct.
		atomic.StoreInt64(&client.lastActive, time.Now().UnixNano())

		// Handle empty heartbeat packets — lastActive is already refreshed above.
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		// Try to parse the packet as JSON
		var packet map[string]interface{}
		if err := json.Unmarshal([]byte(line), &packet); err != nil {
			log.Errorf("failed to parse socket client packet: %v", err)
			continue
		}

		// Check the header
		header, ok := packet["header"].(string)
		if !ok {
			log.Errorf("missing packet header")
			continue
		}

		// Handle the packet based on its header
		switch header {
		case PacketBroadcast:
			// Forward the packet to all clients in the room
			clients := room.getAllClients()
			for _, c := range clients {
				_, err := c.writer.WriteString(line)
				if err != nil {
					log.Errorf("error writing join packet for client: %s, %v", c.name, err)
				}
				err = c.writer.Flush()
				if err != nil {
					log.Errorf("error flushing join packet for client: %s, %v", c.name, err)
				}
			}
		case PacketLeave:
			log.Infof("client %s sent graceful LEAVE", client.name)
			client.once.Do(func() {
				notifyLeave(room, client)
			})
			client.conn.Close()
			return // exits processClientMessages goroutine cleanly
		default:
			log.Infof("unknown packet header: %s", header)
		}
	}
}

// monitorConnection watches for idle clients. It exits as soon as
// processClientMessages closes client.done, preventing spurious
// "timed out" log lines for clients that already disconnected cleanly.
func monitorConnection(client *Client, room *Room) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-client.done:
			// processClientMessages already handled the disconnect.
			return
		case <-ticker.C:
			lastActive := time.Unix(0, atomic.LoadInt64(&client.lastActive))
			if time.Since(lastActive) > 45*time.Second {
				log.Infof("socket client %s timed out", client.name)
				client.once.Do(func() {
					notifyLeave(room, client)
				})
				client.conn.Close()
				return
			}
		}
	}
}

func RegisterNewSocketServer(host, port string) {
	server := NewSocketServer()
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", host, port))
	if err != nil {
		log.Fatalf("failed to start server on %s:%s err: %v", host, port, err)
	}

	log.Infof("socket server started on %s:%s", host, port)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Errorf("failed to accept connections to socket server: %v", err)
			continue
		}

		go handleClient(conn, server)
	}
}
