package main

import (
	"fmt"
	"log"
	"net"

	"github.com/charmbracelet/charm"
)

type Chatroom struct {
	clients     map[net.Addr]charm.Client
	messageChan chan string
}

func main() {
	listener, err := net.Listen("tcp", "localhost:2222")
	if err != nil {
		log.Fatalf("Failed to listen on port 2222: %v", err)
	}

	fmt.Println("Chatroom server is running on port 2222...")

	chatroom := &Chatroom{
		clients:     make(map[net.Addr]charm.Client),
		messageChan: make(chan string),
	}

	go chatroom.broadcastMessages()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatalf("Failed to accept incoming connection: %v", err)
		}

		client := charm.NewClient(conn, charm.DefaultTerminalSize)
		chatroom.clients[conn.RemoteAddr()] = client

		go chatroom.handleConnection(conn, client)
	}
}

func (c *Chatroom) handleConnection(conn net.Conn, client charm.Client) {
	defer func() {
		conn.Close()
		delete(c.clients, conn.RemoteAddr())
	}()

	client.Println("Welcome to the chatroom!")

	for {
		message, err := client.ReadLine()
		if err != nil {
			log.Printf("Failed to read from client: %v", err)
			return
		}

		c.messageChan <- message
	}
}

func (c *Chatroom) broadcastMessages() {
	for message := range c.messageChan {
		for _, client := range c.clients {
			client.Println(message)
		}
	}
}
