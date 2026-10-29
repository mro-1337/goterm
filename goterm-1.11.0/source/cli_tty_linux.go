//go:build cli && linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

type ttyState struct {
	fd   int
	orig syscall.Termios
	on   bool
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func (t *ttyState) raw() error {
	t.fd = int(os.Stdin.Fd())
	if err := ioctl(t.fd, syscall.TCGETS, unsafe.Pointer(&t.orig)); err != nil {
		return err
	}
	r := t.orig
	r.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	r.Oflag &^= syscall.OPOST
	r.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	r.Cflag &^= syscall.CSIZE | syscall.PARENB
	r.Cflag |= syscall.CS8
	r.Cc[syscall.VMIN] = 1
	r.Cc[syscall.VTIME] = 0
	if err := ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&r)); err != nil {
		return err
	}
	t.on = true
	return nil
}

func (t *ttyState) restore() {
	if t.on {
		ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&t.orig))
		t.on = false
	}
}

func ttySize() (cols, rows int) {
	var ws struct{ row, col, x, y uint16 }
	if err := ioctl(int(os.Stdout.Fd()), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil || ws.col == 0 {
		return 80, 25
	}
	return int(ws.col), int(ws.row)
}

func isTTY(f *os.File) bool {
	var t syscall.Termios
	return ioctl(int(f.Fd()), syscall.TCGETS, unsafe.Pointer(&t)) == nil
}
