//go:build windows

package main

import (
	"time"
	"unsafe"
)

// Windows output: winmm waveOut with a small ring of buffers; Write
// blocks until a buffer frees up, which paces the mixer in real time.

var (
	pWaveOutOpen      = winmm.NewProc("waveOutOpen")
	pWaveOutClose     = winmm.NewProc("waveOutClose")
	pWaveOutPrepare   = winmm.NewProc("waveOutPrepareHeader")
	pWaveOutUnprepare = winmm.NewProc("waveOutUnprepareHeader")
	pWaveOutWrite     = winmm.NewProc("waveOutWrite")
	pWaveOutReset     = winmm.NewProc("waveOutReset")
)

type waveFormatEx struct {
	tag        uint16
	channels   uint16
	rate       uint32
	avgBytes   uint32
	blockAlign uint16
	bits       uint16
	cbSize     uint16
}

type waveHdr struct {
	data     uintptr
	length   uint32
	recorded uint32
	user     uintptr
	flags    uint32
	loops    uint32
	next     uintptr
	reserved uintptr
}

const (
	whdrDone     = 0x1
	whdrPrepared = 0x2
	waveBufs     = 4
)

type waveSink struct {
	h    uintptr
	hdr  [waveBufs]*waveHdr
	buf  [waveBufs][]byte
	next int
}

func openWaveSink() audioSink {
	f := waveFormatEx{tag: 1, channels: 2, rate: apRate, avgBytes: apRate * 4, blockAlign: 4, bits: 16}
	var h uintptr
	r, _, _ := pWaveOutOpen.Call(uintptr(unsafe.Pointer(&h)), 0xFFFFFFFF, uintptr(unsafe.Pointer(&f)), 0, 0, 0)
	if r != 0 {
		return nil
	}
	return &waveSink{h: h}
}

func (w *waveSink) Write(p []byte) error {
	i := w.next
	w.next = (w.next + 1) % waveBufs
	hd := w.hdr[i]
	if hd != nil {
		for hd.flags&whdrDone == 0 {
			time.Sleep(2 * time.Millisecond)
		}
		pWaveOutUnprepare.Call(w.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
	} else {
		hd = &waveHdr{}
		w.hdr[i] = hd
	}
	if cap(w.buf[i]) < len(p) {
		w.buf[i] = make([]byte, len(p))
	}
	w.buf[i] = w.buf[i][:len(p)]
	copy(w.buf[i], p)
	*hd = waveHdr{data: uintptr(unsafe.Pointer(&w.buf[i][0])), length: uint32(len(p))}
	pWaveOutPrepare.Call(w.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
	pWaveOutWrite.Call(w.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
	return nil
}

func (w *waveSink) Close() {
	pWaveOutReset.Call(w.h)
	for _, hd := range w.hdr {
		if hd != nil {
			pWaveOutUnprepare.Call(w.h, uintptr(unsafe.Pointer(hd)), unsafe.Sizeof(*hd))
		}
	}
	pWaveOutClose.Call(w.h)
}

func platformAudioSink() func() audioSink { return openWaveSink }
