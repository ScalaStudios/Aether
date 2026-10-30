package api

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const spfLookupLimit = 10

var errSPFTemp = errors.New("spf temporary error")

type spfEval struct {
	resolve resolver
	lookups int
}

func (s *Server) spfStatus(ctx context.Context, domain string) string {
	hosts, err := s.resolve.LookupHost(ctx, s.cfg.MailHostname)
	if err != nil || len(hosts) == 0 {
		return "error"
	}
	status := "ok"
	for _, h := range hosts {
		ip, err := netip.ParseAddr(h)
		if err != nil {
			continue
		}
		e := &spfEval{resolve: s.resolve}
		switch e.check(ctx, domain, ip.Unmap(), 0) {
		case "pass":
		case "fail":
			return "mismatch"
		case "temperror":
			return "error"
		default:
			status = "warning"
		}
	}
	return status
}

func (e *spfEval) count() bool {
	e.lookups++
	return e.lookups <= spfLookupLimit
}

func (e *spfEval) check(ctx context.Context, domain string, ip netip.Addr, depth int) string {
	if depth > spfLookupLimit {
		return "permerror"
	}
	txts, err := e.resolve.LookupTXT(ctx, domain)
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		return "temperror"
	}
	var record string
	for _, t := range txts {
		if lower := strings.ToLower(t); lower == "v=spf1" || strings.HasPrefix(lower, "v=spf1 ") {
			if record != "" {
				return "permerror"
			}
			record = lower
		}
	}
	if record == "" {
		return "none"
	}
	redirect := ""
	for _, term := range strings.Fields(record)[1:] {
		if name, value, ok := strings.Cut(term, "="); ok && !strings.ContainsAny(name, ":/") {
			if name == "redirect" {
				redirect = value
			}
			continue
		}
		qualifier := "pass"
		switch term[0] {
		case '+':
			term = term[1:]
		case '-':
			qualifier, term = "fail", term[1:]
		case '~':
			qualifier, term = "softfail", term[1:]
		case '?':
			qualifier, term = "neutral", term[1:]
		}
		matched, result := e.mechanism(ctx, domain, term, ip, depth)
		if result != "" {
			return result
		}
		if matched {
			return qualifier
		}
	}
	if redirect != "" {
		if !e.count() {
			return "permerror"
		}
		if r := e.check(ctx, redirect, ip, depth+1); r != "none" {
			return r
		}
		return "permerror"
	}
	return "neutral"
}

func (e *spfEval) mechanism(ctx context.Context, domain, term string, ip netip.Addr, depth int) (bool, string) {
	name, arg, _ := strings.Cut(term, ":")
	if strings.Contains(arg, "%") {
		return false, "permerror"
	}
	switch {
	case name == "all":
		return true, ""
	case name == "include":
		if !e.count() {
			return false, "permerror"
		}
		switch r := e.check(ctx, arg, ip, depth+1); r {
		case "pass":
			return true, ""
		case "fail", "softfail", "neutral":
			return false, ""
		case "none":
			return false, "permerror"
		default:
			return false, r
		}
	case name == "ip4" || name == "ip6":
		prefix, err := parsePrefix(arg)
		if err != nil {
			return false, "permerror"
		}
		return prefix.Contains(ip), ""
	case name == "a" || strings.HasPrefix(name, "a/") || name == "mx" || strings.HasPrefix(name, "mx/"):
		mech, _, _ := strings.Cut(name, "/")
		rest := term[len(mech):]
		target := domain
		if spec, ok := strings.CutPrefix(rest, ":"); ok {
			target, rest = spec, ""
			if i := strings.Index(spec, "/"); i >= 0 {
				target, rest = spec[:i], spec[i:]
			}
		}
		v4, v6, err := dualCIDR(rest)
		if err != nil {
			return false, "permerror"
		}
		if !e.count() {
			return false, "permerror"
		}
		hosts := []string{target}
		if mech == "mx" {
			mxs, err := e.resolve.LookupMX(ctx, target)
			if err := dnsFailure(err); err != nil {
				return false, "temperror"
			}
			hosts = hosts[:0]
			for _, mx := range mxs[:min(len(mxs), 10)] {
				hosts = append(hosts, strings.TrimSuffix(mx.Host, "."))
			}
		}
		for _, h := range hosts {
			addrs, err := e.resolve.LookupHost(ctx, h)
			if err := dnsFailure(err); err != nil {
				return false, "temperror"
			}
			for _, a := range addrs {
				addr, err := netip.ParseAddr(a)
				if err != nil {
					continue
				}
				bits := v6
				if addr.Unmap().Is4() {
					bits = v4
				}
				if p, err := addr.Unmap().Prefix(bits); err == nil && p.Contains(ip) {
					return true, ""
				}
			}
		}
		return false, ""
	case name == "exists":
		if !e.count() {
			return false, "permerror"
		}
		addrs, err := e.resolve.LookupHost(ctx, arg)
		if err := dnsFailure(err); err != nil {
			return false, "temperror"
		}
		return len(addrs) > 0, ""
	case name == "ptr":
		if !e.count() {
			return false, "permerror"
		}
		return false, ""
	}
	return false, "permerror"
}

func dnsFailure(err error) error {
	var dnsErr *net.DNSError
	if err == nil || errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil
	}
	return errSPFTemp
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return addr.Unmap().Prefix(addr.Unmap().BitLen())
}

func dualCIDR(s string) (int, int, error) {
	v4s, v6s, _ := strings.Cut(s, "//")
	v4s = strings.TrimPrefix(v4s, "/")
	v4, v6 := 32, 128
	var err error
	if v4s != "" {
		if v4, err = strconv.Atoi(v4s); err != nil || v4 > 32 {
			return 0, 0, errors.New("bad cidr")
		}
	}
	if v6s != "" {
		if v6, err = strconv.Atoi(v6s); err != nil || v6 > 128 {
			return 0, 0, errors.New("bad cidr")
		}
	}
	return v4, v6, nil
}
