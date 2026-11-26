package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Port turns config.json's port (a number, or a string of digits) into one.
func Port(v any) (int, error) {
	switch x := v.(type) {
	case float64:
		if x == float64(int(x)) && x >= 0 && x < 65536 {
			return int(x), nil
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && n >= 0 && n < 65536 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("port %v isn't a port number", v)
}

// Host is config.json's host as a bind address; anything that isn't a
// string means every interface.
func Host(v any) string {
	s, _ := v.(string)
	return s
}

// Listen binds host:port, and if that port is taken tries the next one, up
// to tries more times, saying so each time. Other errors stop at once.
func Listen(host string, port, tries int, say func(string)) (net.Listener, int, error) {
	for {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return l, l.Addr().(*net.TCPAddr).Port, nil
		}
		if !addrInUse(err) || tries <= 0 {
			return nil, port, err
		}
		say(fmt.Sprintf("Port %d is busy — trying %d…", port, port+1))
		port++
		tries--
	}
}

func addrInUse(err error) bool {
	var se *os.SyscallError
	if errors.As(err, &se) {
		var errno syscall.Errno
		if errors.As(se.Err, &errno) {
			return errno == syscall.EADDRINUSE || errno == wsaeaddrinuse
		}
	}
	return errors.Is(err, syscall.EADDRINUSE)
}

// wsaeaddrinuse is Windows' "address already in use".
const wsaeaddrinuse = syscall.Errno(10048)

// Interfaces lists this machine's addresses; a seam for tests.
var Interfaces = net.InterfaceAddrs

// LANOrigins is where the overlay can be reached from other devices: the
// configured host when it's a real address, every non-loopback IPv4 address
// when bound to all interfaces, none on loopback.
func LANOrigins(host any, port int) []string {
	out := []string{}
	h, _ := host.(string)
	if h != "" && h != "0.0.0.0" && h != "::" {
		if h == "127.0.0.1" || h == "localhost" {
			return out
		}
		return append(out, fmt.Sprintf("http://%s:%d", h, port))
	}
	addrs, err := Interfaces()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
			out = append(out, fmt.Sprintf("http://%s:%d", ip4, port))
		}
	}
	return out
}

// Banner is what's printed once the server is listening. The URLs always say
// 127.0.0.1: that's where the streamer opens them.
func Banner(port int, lan []string, notes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "duplexchat is running:\n")
	fmt.Fprintf(&b, "  Control panel:     http://127.0.0.1:%d/\n", port)
	fmt.Fprintf(&b, "  OBS overlay:       http://127.0.0.1:%d/overlay\n", port)
	fmt.Fprintf(&b, "  Reply dock:        http://127.0.0.1:%d/dock\n", port)
	fmt.Fprintf(&b, "  Stream info:       http://127.0.0.1:%d/info\n", port)
	for _, n := range notes {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	for _, o := range lan {
		fmt.Fprintf(&b, "  On the local network: %s/  (panel)  ·  %s/overlay\n", o, o)
	}
	return b.String()
}
