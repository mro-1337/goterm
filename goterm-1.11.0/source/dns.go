package main

import (
	"context"
	"net"
	"strings"
	"time"
)

// setupDNS makes the pure-Go resolver query the given servers (comma
// separated IPs) instead of reading /etc/resolv.conf, which Android lacks.
func setupDNS(list string) {
	var servers []string
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, ":") || strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
			s = net.JoinHostPort(strings.Trim(s, "[]"), "53")
		}
		servers = append(servers, s)
	}
	if len(servers) == 0 {
		return
	}
	i := 0
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 4 * time.Second}
			var err error
			for k := 0; k < len(servers); k++ {
				var c net.Conn
				c, err = d.DialContext(ctx, network, servers[(i+k)%len(servers)])
				if err == nil {
					return c, nil
				}
			}
			i++
			return nil, err
		},
	}
}

func defaultResolverPreferGo() bool { return net.DefaultResolver != nil && net.DefaultResolver.PreferGo }
