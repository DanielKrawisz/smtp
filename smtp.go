package main

import (
	"bufio"
	"container/list"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mailhog/data"
	"github.com/mailhog/smtp"
)

// smtpRun handles a smtp session through a tcp connection.
// May be run as a own goroutine if you want to do something else while the
// session runs.
func smtpRun(smtp *smtp.Protocol, conn net.Conn) {
	reader := bufio.NewReader(conn)

	// smtp begins with a reply code 220.
	reply := smtp.Start()

	// loop through the pattern of smtp interactions.
	for {
		if reply != nil {
			// Send the latest reply.
			for _, r := range reply.Lines() {
				_, err := conn.Write([]byte(r))
				if err != nil {
					break
				}
			}
		}

		// read a line of text from the stream.
		command, err := reader.ReadString([]byte("\n")[0])
		if err != nil {
			break
		}

		// command is exactly one line of text, so Parse will never return
		// any remaining string we have to worry about.
		_, reply = smtp.Parse(string(command))
	}
}

// smtpServer provides an smtp server for handling communications with smtp clients.
type smtpServer struct {
	wait        sync.WaitGroup
	listener    net.Listener
	connections *list.List
	started     int32 // atomic
	shutdown    int32 // atomic
	lock        sync.RWMutex
	maxConn     int // The maximum number of allowed connection.

	//Channels for the goroutines to communicate with one another.
	newConnChan chan net.Conn
	quit        chan struct{}
}

// Must be run as a goroutine.
func (serv *smtpServer) listen(listener net.Listener) {
	serv.wait.Add(1)
	defer serv.wait.Done()

	for atomic.LoadInt32(&serv.shutdown) == 0 {
		conn, err := serv.listener.Accept()
		if err != nil {
			conn.Close()
			continue
		}

		serv.newConnChan <- conn
	}
}

// connectionHandler handles all smtp sessions currently running.
// Must be run as a goroutine.
func (serv *smtpServer) connectionHandler() {
	serv.wait.Add(1)
	defer serv.wait.Done()

	connections := make(map[net.Conn]struct{}) // The list of simultaneous connections.
	doneSessionChan := make(chan net.Conn)

	quit := false // Whether the handler has received the message to quit.

	for {
		select {
		case <-serv.quit:
			quit = true
			if len(connections) == 0 {
				return
			}

			for conn := range connections {
				conn.Close()
			}

		// A new connection is being initiated.
		case conn := <-serv.newConnChan:
			// Don't allow more than the maximum number of simultaneous sessions.
			if len(connections) >= serv.maxConn {
				conn.Close()
				continue
			}

			connections[conn] = struct{}{}

			// Set up the smtp state machine.
			smtp := smtp.NewProtocol()
			// What to do with an email that is received.
			smtp.MessageReceivedHandler = func(message *data.Message) (string, error) {
				fmt.Println("Message received:", message.Content.Body)
				return string(message.ID), nil
			}

			// start running the protocol.
			go func() {
				smtpRun(smtp, conn)
				doneSessionChan <- conn
			}()

		// A session has finished.
		case conn := <-doneSessionChan:
			conn.Close()

			delete(connections, conn)
			if len(connections) == 0 && quit {
				return
			}
		}
	}
}

// Start starts the smtpServer object.
func (serv *smtpServer) Start(addr string) {
	if atomic.AddInt32(&serv.started, 1) != 1 {
		return
	}

	var err error
	serv.listener, err = net.Listen("tcp4", addr)

	if err != nil {
		atomic.StoreInt32(&serv.started, 0)
		return
	}

	go serv.listen(serv.listener)
	go serv.connectionHandler()
}

// Stop stops and cleans up the smtpServer object.
func (serv *smtpServer) Stop() {
	if atomic.AddInt32(&serv.shutdown, 1) != 1 {
		return
	}

	serv.listener.Close()
	close(serv.quit)
	serv.wait.Wait()

	atomic.StoreInt32(&serv.started, 0)
}

// NewSMTPServer returns a new smtp server.
func NewSMTPServer(maxConn int) *smtpServer {
	return &smtpServer{
		connections: list.New(),
		maxConn:     maxConn,
		quit:        make(chan struct{}),
		newConnChan: make(chan net.Conn),
	}
}

func main() {
	server := NewSMTPServer(5)
	fmt.Println("Starting smtp server.")
	server.Start(net.JoinHostPort("", "25"))

	time.Sleep(time.Minute * 20)
	server.Stop()
}
