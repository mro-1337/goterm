package main

import (
	"strconv"
	"strings"
)

// startArgs holds the command line shared by the window and terminal clients.
type startArgs struct {
	Target, Mode, Term, User string
	Bad                      bool
}

// parseStartArgs understands:  [-mode M] [-term T] [-user U] [target | host port]
func parseStartArgs(args []string) startArgs {
	var s startArgs
	var rest []string
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			s.Bad = true
			return ""
		}
		switch args[i] {
		case "-mode":
			s.Mode = next()
		case "-term":
			s.Term = next()
		case "-user":
			s.User = next()
		default:
			if strings.HasPrefix(args[i], "-") {
				s.Bad = true
			} else {
				rest = append(rest, args[i])
			}
		}
	}
	switch len(rest) {
	case 0:
	case 2:
		if _, err := strconv.Atoi(rest[1]); err == nil { // "host port"
			s.Target = rest[0] + ":" + rest[1]
			break
		}
		s.Bad = true
	case 1:
		s.Target = rest[0]
	default:
		s.Bad = true
	}
	return s
}

func entryFromTarget(cfg *Config, t string) DialEntry {
	for _, e := range cfg.Entries {
		if strings.EqualFold(e.Name, t) {
			return e
		}
	}
	e := DialEntry{Name: t, Host: t, Port: 23, Conn: "telnet", Mode: cfg.Mode}
	s := t
	if i := strings.Index(s, "://"); i > 0 {
		switch strings.ToLower(s[:i]) {
		case "ssh", "rlogin", "telnet":
			e.Conn = strings.ToLower(s[:i])
		}
		s = s[i+3:]
		e.Port = defaultPort(e.Conn)
	}
	if j := strings.LastIndex(s, "@"); j > 0 {
		e.User, s = s[:j], s[j+1:]
	}
	e.Host = s
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		if p, err := strconv.Atoi(s[i+1:]); err == nil {
			e.Port = p
			e.Host = s[:i]
		}
	}
	return e
}
