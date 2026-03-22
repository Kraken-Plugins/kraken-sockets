package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
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
	lastActive    time.Time
	writer        *bufio.Writer
	reader        *bufio.Reader
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
		lastActive: time.Now(),
		writer:     writer,
		reader:     reader,
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

	// Decrypt the player name for server-side logging
	// TODO The room id is SHA256 encrypted but we can't decrypt it correctly? so then we cant decrypt player names correctly
	// etc... gotta figure out how this works.

	//password := strings.Replace(roomID, Hash(PasswordSalt), "", 1)
	//key := password + PasswordSalt
	client.name = DecryptAES(roomID, encryptedName)

	log.Infof("player %s joined room %s", client.name, roomID)
	room.addClient(client)
	notifyJoin(room, client)

	// Process messages from the client
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

// Process messages from a client
func processClientMessages(client *Client, room *Room) {
	for {
		// Read a line from the client
		conn := client.conn
		reader := client.reader

		// Set a read deadline for detecting disconnects
		err := conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		if err != nil {
			log.Errorf("failed to set read deadline: %v", err)
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			log.Errorf("client %s disconnected: %v", client.name, err)
			client.once.Do(func() { notifyLeave(room, client) })
			return
		}

		// Only reset lastActive after a confirmed successful read
		client.lastActive = time.Now()

		// Handle empty heartbeat packets
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

// Monitor the client connection
func monitorConnection(client *Client, room *Room) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C

		// Check if the client is still active
		if time.Since(client.lastActive) > 45*time.Second {
			log.Infof("socket client %s timed out", client.name)
			client.once.Do(func() {
				notifyLeave(room, client)
			})
			client.conn.Close()
			return
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
