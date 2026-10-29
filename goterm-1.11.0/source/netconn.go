package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Conn is what a Session talks to: telnet, rlogin or ssh.
type Conn interface {
	io.ReadWriteCloser
	SetSize(cols, rows int)
}

func defaultPort(conn string) int {
	switch conn {
	case "ssh":
		return 22
	case "rlogin":
		return 513
	}
	return 23
}

// ---------------------------------------------------------------- rlogin

type RLoginConn struct {
	nc    net.Conn
	first bool
}

func (r *RLoginConn) SetSize(cols, rows int)      {}
func (r *RLoginConn) Close() error                { return r.nc.Close() }
func (r *RLoginConn) Write(p []byte) (int, error) { return r.nc.Write(p) }

func (r *RLoginConn) Read(p []byte) (int, error) { return r.nc.Read(p) }

// DialRLogin connects and performs the RFC 1282 handshake:
// "\0" client-user "\0" server-user "\0" terminal/speed "\0", answered by "\0".
func DialRLogin(addr, user, term string, timeout time.Duration) (*RLoginConn, error) {
	nc, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	if user == "" {
		user = "guest"
	}
	if term == "" {
		term = "ansi-bbs"
	}
	hello := "\x00" + user + "\x00" + user + "\x00" + term + "/38400\x00"
	nc.SetDeadline(time.Now().Add(timeout))
	if _, err := nc.Write([]byte(hello)); err != nil {
		nc.Close()
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(nc, b[:]); err != nil {
		nc.Close()
		return nil, fmt.Errorf("rlogin handshake: %w", err)
	}
	nc.SetDeadline(time.Time{})
	if b[0] != 0 {
		// Some servers skip the ACK byte; treat it as data.
		return &RLoginConn{nc: &prefixConn{Conn: nc, pre: b[:]}}, nil
	}
	return &RLoginConn{nc: nc}, nil
}

type prefixConn struct {
	net.Conn
	pre []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.pre) > 0 {
		n := copy(b, p.pre)
		p.pre = p.pre[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// ---------------------------------------------------------------- ssh

type SSHConn struct {
	cl   *ssh.Client
	sess *ssh.Session
	in   io.WriteCloser
	out  *io.PipeReader
}

func (c *SSHConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *SSHConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *SSHConn) SetSize(cols, rows int)      { c.sess.WindowChange(rows, cols) }
func (c *SSHConn) Close() error {
	c.sess.Close()
	return c.cl.Close()
}

func knownHostsPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		return ""
	}
	return filepath.Join(d, "GoTerm", "known_hosts")
}

// hostKeyCallback implements trust-on-first-use: unknown hosts are recorded,
// a changed key is refused.
func hostKeyCallback() ssh.HostKeyCallback {
	path := knownHostsPath()
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		if path == "" {
			return nil
		}
		os.MkdirAll(filepath.Dir(path), 0o700)
		if _, err := os.Stat(path); err != nil {
			os.WriteFile(path, nil, 0o600)
		}
		cb, err := knownhosts.New(path)
		if err != nil {
			return err
		}
		err = cb(host, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
				if e != nil {
					return e
				}
				defer f.Close()
				_, e = fmt.Fprintln(f, knownhosts.Line([]string{host}, key))
				return e
			}
			return fmt.Errorf("HOST KEY CHANGED for %s - refusing to connect (edit %s if this is expected)", host, path)
		}
		return err
	}
}

func localKeys() []ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []ssh.Signer
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		b, err := os.ReadFile(filepath.Join(home, ".ssh", n))
		if err != nil {
			continue
		}
		if s, err := ssh.ParsePrivateKey(b); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// DialSSH connects, authenticates, opens a pty and a shell. askPass is
// called when a password is needed and none is stored.
func DialSSH(addr, user, pass, term string, cols, rows int, timeout time.Duration,
	askPass func() (string, error)) (*SSHConn, error) {
	if user == "" {
		user = "guest"
	}
	var cached *string
	getPass := func() (string, error) {
		if pass != "" {
			return pass, nil
		}
		if cached != nil {
			return *cached, nil
		}
		p, err := askPass()
		if err != nil {
			return "", err
		}
		cached = &p
		return p, nil
	}
	var methods []ssh.AuthMethod
	if ks := localKeys(); len(ks) > 0 {
		methods = append(methods, ssh.PublicKeys(ks...))
	}
	methods = append(methods,
		ssh.PasswordCallback(getPass),
		ssh.KeyboardInteractive(func(name, instr string, qs []string, echos []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range qs {
				p, err := getPass()
				if err != nil {
					return nil, err
				}
				ans[i] = p
			}
			return ans, nil
		}))
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		HostKeyCallback: hostKeyCallback(),
		Timeout:         timeout,
	}
	cl, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	sess, err := cl.NewSession()
	if err != nil {
		cl.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout = pw
	sess.Stderr = pw
	in, err := sess.StdinPipe()
	if err != nil {
		cl.Close()
		return nil, err
	}
	if term == "" {
		term = "ansi"
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := sess.RequestPty(term, rows, cols, modes); err != nil {
		cl.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		cl.Close()
		return nil, err
	}
	go func() {
		err := sess.Wait()
		pw.CloseWithError(io.EOF)
		_ = err
	}()
	return &SSHConn{cl: cl, sess: sess, in: in, out: pr}, nil
}

var _ = strconv.Itoa
var _ = strings.TrimSpace
