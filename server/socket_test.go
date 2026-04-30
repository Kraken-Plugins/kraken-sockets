package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebsocketServerJoinBroadcastLeaveFlow(t *testing.T) {
	socketServer := &SocketServer{
		rooms: make(map[string]*Room),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}

		handleClient(w, r, socketServer)
	})

	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	websocketURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
	roomID := "room-1"
	aliceName := EncryptAES(roomID, "alice")
	bobName := EncryptAES(roomID, "bob")

	aliceConn := dialTestClient(t, websocketURL)
	defer aliceConn.Close()

	mustWritePacket(t, aliceConn, map[string]interface{}{
		"header": PacketJoin,
		"room":   roomID,
		"name":   aliceName,
	})

	joinPacket := mustReadPacket(t, aliceConn)
	assertPacketHeader(t, joinPacket, PacketJoin)
	assertPartyMembers(t, joinPacket, []string{aliceName})

	bobConn := dialTestClient(t, websocketURL)
	defer bobConn.Close()

	mustWritePacket(t, bobConn, map[string]interface{}{
		"header": PacketJoin,
		"room":   roomID,
		"name":   bobName,
	})

	aliceJoinUpdate := mustReadPacket(t, aliceConn)
	assertPacketHeader(t, aliceJoinUpdate, PacketJoin)
	assertPacketPlayer(t, aliceJoinUpdate, bobName)
	assertPartyMembers(t, aliceJoinUpdate, []string{aliceName, bobName})

	bobJoinUpdate := mustReadPacket(t, bobConn)
	assertPacketHeader(t, bobJoinUpdate, PacketJoin)
	assertPacketPlayer(t, bobJoinUpdate, bobName)
	assertPartyMembers(t, bobJoinUpdate, []string{aliceName, bobName})

	broadcastPacket := map[string]interface{}{
		"header": PacketBroadcast,
		"player": aliceName,
		"body":   "hello",
	}
	mustWritePacket(t, aliceConn, broadcastPacket)

	aliceBroadcast := mustReadPacket(t, aliceConn)
	assertPacketHeader(t, aliceBroadcast, PacketBroadcast)
	assertPacketPlayer(t, aliceBroadcast, aliceName)

	bobBroadcast := mustReadPacket(t, bobConn)
	assertPacketHeader(t, bobBroadcast, PacketBroadcast)
	assertPacketPlayer(t, bobBroadcast, aliceName)

	mustWritePacket(t, aliceConn, map[string]interface{}{
		"header": PacketLeave,
	})

	leavePacket := mustReadPacket(t, bobConn)
	assertPacketHeader(t, leavePacket, PacketLeave)
	assertPacketPlayer(t, leavePacket, aliceName)
	assertPartyMembers(t, leavePacket, []string{bobName})
}

func dialTestClient(t *testing.T, websocketURL string) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL, nil)
	if err != nil {
		t.Fatalf("failed to dial test websocket server: %v", err)
	}

	return conn
}

func mustWritePacket(t *testing.T, conn *websocket.Conn, packet map[string]interface{}) {
	t.Helper()

	err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("failed to set test write deadline: %v", err)
	}

	if err := conn.WriteJSON(packet); err != nil {
		t.Fatalf("failed to write packet: %v", err)
	}
}

func mustReadPacket(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()

	err := conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("failed to set test read deadline: %v", err)
	}

	packet := make(map[string]interface{})
	if err := conn.ReadJSON(&packet); err != nil {
		t.Fatalf("failed to read packet: %v", err)
	}

	return packet
}

func assertPacketHeader(t *testing.T, packet map[string]interface{}, expected string) {
	t.Helper()

	header, ok := packet["header"].(string)
	if !ok {
		t.Fatalf("packet header missing or invalid: %#v", packet["header"])
	}

	if header != expected {
		t.Fatalf("expected header %s, got %s", expected, header)
	}
}

func assertPacketPlayer(t *testing.T, packet map[string]interface{}, expected string) {
	t.Helper()

	player, ok := packet["player"].(string)
	if !ok {
		t.Fatalf("packet player missing or invalid: %#v", packet["player"])
	}

	if player != expected {
		t.Fatalf("expected player %s, got %s", expected, player)
	}
}

func assertPartyMembers(t *testing.T, packet map[string]interface{}, expected []string) {
	t.Helper()

	party, ok := packet["party"].([]interface{})
	if !ok {
		t.Fatalf("packet party missing or invalid: %#v", packet["party"])
	}

	memberNames := make([]string, 0, len(party))
	for _, member := range party {
		memberName, ok := member.(string)
		if !ok {
			t.Fatalf("party member missing or invalid: %#v", member)
		}

		memberNames = append(memberNames, memberName)
	}

	slices.Sort(memberNames)
	sortedExpected := slices.Clone(expected)
	slices.Sort(sortedExpected)

	if !slices.Equal(memberNames, sortedExpected) {
		t.Fatalf("expected party %v, got %v", sortedExpected, memberNames)
	}
}
