//go:build !windows

package main

import (
	"io"
	"os/exec"
	"sync"
)

// On Linux and friends the mixed PCM is piped to whichever command-line
// player is installed.
var pipePlayers = [][]string{
	{"paplay", "--raw", "--rate=44100", "--channels=2", "--format=s16le", "--latency-msec=80"},
	{"pw-cat", "-p", "--rate", "44100", "--channels", "2", "--format", "s16", "-"},
	{"aplay", "-q", "-t", "raw", "-f", "S16_LE", "-r", "44100", "-c", "2"},
	{"play", "-q", "-t", "raw", "-r", "44100", "-e", "signed", "-b", "16", "-c", "2", "-"},
}

type pipeSink struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	once sync.Once
}

func openPipeSink() audioSink {
	for _, c := range pipePlayers {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		in, err := cmd.StdinPipe()
		if err != nil {
			continue
		}
		if err := cmd.Start(); err != nil {
			continue
		}
		return &pipeSink{cmd: cmd, in: in}
	}
	return nil
}

func (p *pipeSink) Write(b []byte) error {
	_, err := p.in.Write(b)
	return err
}

func (p *pipeSink) Close() {
	p.once.Do(func() {
		p.in.Close()
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
		p.cmd.Wait()
	})
}

func platformAudioSink() func() audioSink { return openPipeSink }
