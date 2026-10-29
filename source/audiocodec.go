package main

import (
	"bytes"
	"io"

	mp3 "github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
)

func decodeMP3(b []byte) (pcm []int16, ch, rate int, ok bool) {
	defer func() {
		if recover() != nil {
			pcm, ok = nil, false
		}
	}()
	d, err := mp3.NewDecoder(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, false
	}
	raw, err := io.ReadAll(io.LimitReader(d, maxAudioB))
	if len(raw) < 4 {
		return nil, 0, 0, false
	}
	pcm = make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}
	return pcm, 2, d.SampleRate(), true
}

func decodeOggVorbis(b []byte) (pcm []int16, ch, rate int, ok bool) {
	defer func() {
		if recover() != nil {
			pcm, ok = nil, false
		}
	}()
	r, err := oggvorbis.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, false
	}
	ch, rate = r.Channels(), r.SampleRate()
	buf := make([]float32, 8192*ch)
	for len(pcm) < maxAudioB/2 {
		n, err := r.Read(buf)
		for _, f := range buf[:n] {
			pcm = append(pcm, clip16(float64(f)))
		}
		if err != nil {
			break
		}
	}
	return pcm, ch, rate, len(pcm) >= ch && ch > 0
}
