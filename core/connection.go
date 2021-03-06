package core

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

const (
	Bufsize int    = 512
	Newline string = "\r\n"
)

type Connection struct {
	netConn net.Conn
	Read    <-chan string
	Write   chan<- string
	Prompt  chan<- bool

	RawWrite   chan<- []byte
	buffer     []byte
	bufferLock sync.Mutex
	echo       bool
}

func (conn *Connection) Close() {
	fmt.Printf("Closed connection to %v\n", conn.netConn.RemoteAddr())
	conn.netConn.Close()
}

func readLines(conn *Connection, r chan<- string, reader io.Reader) {
	defer close(r)
	b := make([]byte, Bufsize)
	for {
		n, err := reader.Read(b)
		if err != nil {
			return
		}
		conn.bufferLock.Lock()
		telnet_option_mode := false
		for i := 0; i < n; i++ {
			next := b[i]
			// TODO: handle esc sequences, aka [*
			switch {
			case next&0x80 != 0:
				// telnet control character
				if next == 0xfd {
					telnet_option_mode = true
				}
			case telnet_option_mode:
				// telnet option
				if next == 0x01 {
					conn.echo = true
				}
				telnet_option_mode = false
			case strconv.IsPrint(rune(next)): // TODO: enforce a max buf size
				// printable character
				if conn.echo {
					conn.RawWrite <- []byte{next}
				}
				conn.buffer = append(conn.buffer, next)
			case next == 0x7f && conn.echo:
				// backspace
				if len(conn.buffer) > 0 {
					conn.buffer = conn.buffer[:len(conn.buffer)-1]
					conn.RawWrite <- []byte("\b \b")
				}
			case len(conn.buffer) > 0:
				// newline
				r <- string(conn.buffer)
				if conn.echo {
					conn.RawWrite <- []byte(Newline)
				}
				conn.buffer = conn.buffer[:0]
			}
		}
		conn.bufferLock.Unlock()
	}
}

func writeRaw(w <-chan []byte, writer io.Writer) {
	for b := range w {
		writer.Write(b)
	}
}

func writeAndPrompt(conn *Connection, wp <-chan string, w chan<- []byte) {
	for s := range wp {
		prefix := Newline
		conn.bufferLock.Lock()
		buf := string(conn.buffer)
		if conn.echo {
			prefix = "\r\x00"
		}
		conn.bufferLock.Unlock()
		if s != "" {
			w <- []byte(prefix + s + Newline)
		}
		w <- []byte("\r\x00> " + buf)
	}
}

func triggerPrompts(p <-chan bool, wp chan<- string) {
	for _ = range p {
		wp <- ""
	}
}

func detachConnection(conn net.Conn) *Connection {
	r := make(chan string)  // read
	w := make(chan []byte)  // raw write
	p := make(chan bool)    // trigger prompt
	wp := make(chan string) // write with prompt
	connection := Connection{conn, r, wp, p, w, make([]byte, 256), sync.Mutex{}, false}
	go readLines(&connection, r, conn)
	go writeRaw(w, conn)
	go writeAndPrompt(&connection, wp, w)
	go triggerPrompts(p, wp)

	// Set telnet to WILL ECHO (disable local buffering/edit). We wait to receive a
	// DO ECHO response before changing behavior.
	w <- []byte{0xff, 0xfb, 0x03, 0xff, 0xfb, 0x01}

	return &connection
}

func acceptConnectionsForever(ln net.Listener, c chan<- *Connection) {
	for {
		if conn, err := ln.Accept(); err == nil {
			fmt.Printf("Accepted new connection from %v\n", conn.RemoteAddr())
			c <- detachConnection(conn)
		}
	}
}

func ServeForever(port uint, c chan<- *Connection) error {
	if ln, err := net.Listen("tcp", fmt.Sprintf(":%v", port)); err != nil {
		return err
	} else {
		fmt.Printf("Listening on port %v\n", port)
		acceptConnectionsForever(ln, c)
		return nil
	}
}
