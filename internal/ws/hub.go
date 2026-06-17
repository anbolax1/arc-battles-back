package ws

import "sync"

// Hub рассылает сообщения всем подключённым клиентам оверлея.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
}

type Client struct {
	Send chan []byte
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*Client]struct{})}
}

// Register создаёт нового клиента с буферизованным каналом.
func (h *Hub) Register() *Client {
	c := &Client{Send: make(chan []byte, 8)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.Send)
	}
	h.mu.Unlock()
}

// Broadcast отправляет сообщение всем клиентам (медленных молча пропускаем).
func (h *Hub) Broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.Send <- msg:
		default:
			// клиент не успевает читать — пропускаем кадр
		}
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
