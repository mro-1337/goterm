package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// apc handles SyncTERM's Application Program Commands (ESC _ ... ESC \).
func (p *Parser) apc(s string) {
	switch {
	case s == "SyncTERM:VER":
		p.reply("\x1b_SyncTERM:VER;" + syncTermVersion() + "\x1b\\")
	case strings.HasPrefix(s, "SyncTERM:C;S;"):
		if p.Audio != nil {
			p.Audio.cacheStore(s[13:])
		}
	case strings.HasPrefix(s, "SyncTERM:C;L"):
		if len(s) == 12 || s[12] == ';' {
			p.cacheList(s)
		}
	case strings.HasPrefix(s, "SyncTERM:Q;JXL"):
		p.reply("\x1b[=1;0-n") // no JPEG XL support
	case strings.HasPrefix(s, "SyncTERM:A;"):
		if p.Audio != nil {
			if ch := p.Audio.Handle(s[11:]); ch >= apFirstCh {
				p.WaitCh = ch
			}
		}
	case strings.HasPrefix(s, "SyncTERM:Q;"):
		p.featureQuery(s[11:])
	}
}

func syncTermVersion() string {
	return "SyncTERM 1.10a (GoTerm " + Version + ")"
}

func (p *Parser) featureQuery(q string) {
	switch {
	case q == "libsndfile":
		p.reply("\x1b[=7;100;1n")
	case strings.HasPrefix(q, "libsndfileFormat;"):
		f := strings.Split(q[17:], ";")
		if len(f) != 2 {
			return
		}
		maj, err1 := strconv.ParseUint(f[0], 0, 32)
		sub, err2 := strconv.ParseUint(f[1], 0, 32)
		if err1 != nil || err2 != nil {
			return
		}
		ok := 0
		if audioFormatSupported(uint32(maj), uint32(sub)) {
			ok = 1
		}
		p.reply(fmt.Sprintf("\x1b[=7;101;%d;%d;%dn", maj, sub, ok))
	}
}

// cacheList answers SyncTERM:C;L[;glob] with "name<TAB>md5" lines.
func (p *Parser) cacheList(s string) {
	const head = "\x1b_SyncTERM:C;L\n"
	if p.Audio == nil || p.Audio.CacheDir == "" {
		p.reply(head + "\x1b\\")
		return
	}
	pat := "*"
	if len(s) > 13 {
		pat = s[13:]
	}
	if cleanCachePath(pat) == "" {
		p.reply(head + "\x1b\\")
		return
	}
	matches, _ := filepath.Glob(filepath.Join(p.Audio.CacheDir, filepath.FromSlash(strings.ReplaceAll(pat, "\\", "/"))))
	sort.Strings(matches)
	var sb strings.Builder
	sb.WriteString(head)
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || st.IsDir() {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		sum := md5.Sum(b)
		sb.WriteString(filepath.Base(m) + "\t" + hex.EncodeToString(sum[:]) + "\n")
	}
	sb.WriteString("\x1b\\")
	p.reply(sb.String())
}
