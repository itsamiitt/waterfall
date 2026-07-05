package alerts

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP egress discipline (doc 10 §5.4): email cannot ride the HTTP egress client, so it re-applies
// the same resolve-then-dial guard natively. The configured host is resolved first, EVERY resolved
// address is checked against the private-range / loopback / link-local / cloud-metadata denylist,
// and the dial targets the vetted IP (not the hostname) so a DNS re-resolution cannot bypass the
// check. A 5s dial timeout + a 10s total-send deadline (conn.SetDeadline across EHLO/STARTTLS/AUTH/
// DATA/QUIT) mean an unresponsive relay can never wedge the notifier loop.
const (
	smtpDialTimeout = 5 * time.Second
	smtpSendBudget  = 10 * time.Second
)

// deliverEmail sends p to cfg's recipients through the guarded SMTP dialer.
func deliverEmail(ctx context.Context, cfg ChannelConfig, p notifyPayload) deliveryResult {
	if cfg.Host == "" || cfg.From == "" || len(cfg.To) == 0 {
		return deliveryResult{note: "email channel missing host/from/to"}
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return deliveryResult{note: "smtp host resolution failed: " + err.Error()}
	}
	if len(ips) == 0 {
		return deliveryResult{note: "smtp host resolved to no addresses"}
	}
	// Fail closed: if ANY resolved address is in a denied range, refuse (SSRF).
	for _, ip := range ips {
		if ipBlocked(ip.IP) {
			return deliveryResult{blocked: true, note: "smtp host resolves to a denied address"}
		}
	}
	vetted := net.JoinHostPort(ips[0].IP.String(), strconv.Itoa(port))

	dialer := &net.Dialer{Timeout: smtpDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", vetted)
	if err != nil {
		return deliveryResult{note: "smtp dial failed: " + err.Error()}
	}
	defer conn.Close()
	// Total-send deadline across the whole dialogue.
	_ = conn.SetDeadline(time.Now().Add(smtpSendBudget))

	if err := sendSMTP(conn, cfg, p); err != nil {
		return deliveryResult{note: "smtp send failed: " + err.Error()}
	}
	return deliveryResult{ok: true, note: "sent"}
}

// sendSMTP drives the SMTP dialogue over an already-vetted, deadline-bound connection. The client is
// created with the configured hostname (for TLS SNI / EHLO identity), not the dialed IP.
func sendSMTP(conn net.Conn, cfg ChannelConfig, p notifyPayload) error {
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()

	if cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, rcpt := range cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(emailBody(cfg, p)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// emailBody renders a minimal RFC 5322 message.
func emailBody(cfg ChannelConfig, p notifyPayload) []byte {
	subject := fmt.Sprintf("[%s] %s: %s", strings.ToUpper(nonEmpty(p.Severity, "alert")), p.State, p.RuleName)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n")
	fmt.Fprintf(&b, "Rule: %s\r\nMetric: %s\r\nState: %s\r\nValue: %g\r\nCorrelation: %s\r\n",
		p.RuleName, p.Metric, p.State, p.Value, p.DedupeKey)
	return []byte(b.String())
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ipBlocked mirrors provider.isBlockedIP (unexported there): loopback, RFC1918 + ULA, link-local
// incl. 169.254.169.254 cloud metadata, multicast, unspecified, CGNAT 100.64/10, and 0.0.0.0/8. A
// nil/unparseable IP is blocked (fail closed).
func ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}
