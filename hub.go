package main

import "time"

// Message represents a single Discord message relayed to the frontend.
type Message struct {
	Author    string    `json:"author"`
	Avatar    string    `json:"avatar"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Hub manages SSE client connections, the message buffer, and fan-out broadcasting.
// All state is owned exclusively by the Run() goroutine — no mutexes needed.
type Hub struct {
	clients        map[chan Message]bool
	broadcast      chan Message
	register       chan chan Message
	unregister     chan chan Message
	buffer         []Message
	bufferSize     int

	// pins subscribers receive a bool: true = pinned, false = unpinned.
	pinsSubs       map[chan bool]bool
	pinsUpdated    chan bool
	registerPins   chan chan bool
	unregisterPins chan chan bool

	// quit is closed to stop the Run() goroutine (e.g. during idle teardown).
	quit chan struct{}

	// onIdle is called (in its own goroutine) when the last SSE client disconnects.
	onIdle func()
}

func NewHub(bufferSize int) *Hub {
	return &Hub{
		clients:        make(map[chan Message]bool),
		broadcast:      make(chan Message, 64),
		register:       make(chan chan Message),
		unregister:     make(chan chan Message),
		buffer:         make([]Message, 0, bufferSize),
		bufferSize:     bufferSize,
		pinsSubs:       make(map[chan bool]bool),
		pinsUpdated:    make(chan bool, 8),
		registerPins:   make(chan chan bool),
		unregisterPins: make(chan chan bool),
		quit:           make(chan struct{}),
	}
}

// Run processes registrations, unregistrations, and broadcasts.
// Must be called in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
		case <-h.quit:
			return

		case client := <-h.register:
			h.clients[client] = true
			// Replay buffered messages so the new client sees recent history.
			for _, msg := range h.buffer {
				select {
				case client <- msg:
				default:
				}
			}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client)
			}
			if len(h.clients) == 0 && h.onIdle != nil {
				go h.onIdle()
			}

		case msg := <-h.broadcast:
			h.appendToBuffer(msg)
			for client := range h.clients {
				select {
				case client <- msg:
				default:
					// Slow client: drop the message rather than block the hub.
				}
			}

		case sub := <-h.registerPins:
			h.pinsSubs[sub] = true

		case sub := <-h.unregisterPins:
			if _, ok := h.pinsSubs[sub]; ok {
				delete(h.pinsSubs, sub)
				close(sub)
			}

		case added := <-h.pinsUpdated:
			for sub := range h.pinsSubs {
				select {
				case sub <- added:
				default:
				}
			}
		}
	}
}

func (h *Hub) appendToBuffer(msg Message) {
	if len(h.buffer) >= h.bufferSize {
		h.buffer = h.buffer[1:]
	}
	h.buffer = append(h.buffer, msg)
}
