package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRLogin(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, _ := ln.Accept()
		buf := make([]byte, 0, 64)
		b := make([]byte, 1)
		zeros := 0
		for zeros < 4 {
			c.Read(b)
			buf = append(buf, b[0])
			if b[0] == 0 {
				zeros++
			}
		}
		got <- string(buf)
		c.Write([]byte{0})
		c.Write([]byte("Welcome\r\n"))
		c.Close()
	}()
	c, err := DialRLogin(ln.Addr().String(), "bob", "ansi-bbs", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != "\x00bob\x00bob\x00ansi-bbs/38400\x00" {
		t.Fatalf("handshake %q", h)
	}
	data, _ := io.ReadAll(c)
	if string(data) != "Welcome\r\n" {
		t.Fatalf("data %q", data)
	}
}

func TestSSH(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	scfg := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if m.User() == "bob" && string(p) == "pw" {
			return nil, nil
		}
		return nil, io.ErrUnexpectedEOF
	}}
	scfg.AddHostKey(signer)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		nc, _ := ln.Accept()
		_, chans, reqs, err := ssh.NewServerConn(nc, scfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for nch := range chans {
			ch, creqs, _ := nch.Accept()
			go func() {
				for r := range creqs {
					r.Reply(true, nil)
					if r.Type == "shell" {
						ch.Write([]byte("hello ssh\r\n"))
						io.Copy(ch, ch) // echo
					}
				}
			}()
		}
	}()
	asked := 0
	c, err := DialSSH(ln.Addr().String(), "bob", "", "ansi", 80, 25, 5*time.Second, func() (string, error) {
		asked++
		return "pw", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if asked != 1 {
		t.Fatalf("asked %d", asked)
	}
	buf := make([]byte, 64)
	n, _ := c.Read(buf)
	if !strings.Contains(string(buf[:n]), "hello ssh") {
		t.Fatalf("got %q", buf[:n])
	}
	c.Write([]byte("ping\xff"))
	n, _ = c.Read(buf)
	if string(buf[:n]) != "ping\xff" {
		t.Fatalf("echo %q", buf[:n])
	}
}
