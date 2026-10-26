package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"jailops.net/pkgops/internal/pki"
)

const pkiUsage = `usage:
  pkgops pki init   --dir DIR [--name CN] [--key-type ecdsa|rsa] [--rsa-bits 4096] [--days 5475]
  pkgops pki issue  server --dir DIR --name HOST[,SAN...] [--days 1825] [--key-type ecdsa|rsa] [--out DIR]
  pkgops pki issue  client --dir DIR --name MACHINE [--days 1825] [--key-type ecdsa|rsa] [--out DIR]
  pkgops pki revoke --dir DIR --name MACHINE | --serial N [--crl-days 30]
  pkgops pki list   --dir DIR
  pkgops pki crl    --dir DIR [--days 30] [--check]
  pkgops pki show   FILE
`

func cmdPKI(args []string) error { return runPKI(os.Stdout, os.Stderr, args) }

func runPKI(out, errw io.Writer, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(errw, pkiUsage)
		return errors.New("pki: subcommand required")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "init":
		fs := newFlags("pki init")
		dir := fs.String("dir", "", "CA directory (keep it offline or on the admin machine)")
		name := fs.String("name", "PkgOps private CA", "CA common name")
		kt := fs.String("key-type", "ecdsa", "ecdsa (P-256) or rsa")
		bits := fs.Int("rsa-bits", 4096, "RSA key size")
		days := fs.Int("days", pki.DefaultCADays, "CA validity in days")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *dir == "" {
			return errors.New("--dir is required")
		}
		ca, err := pki.Init(*dir, pki.InitOptions{Name: *name, KeyType: *kt, RSABits: *bits, Days: *days})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "CA certificate: %s\nCA key:         %s (mode 0400; never copy it to a server)\nstate:          %s\nCRL:            %s\nvalid until:    %s\nsha256:         %s\n",
			p(*dir, pki.CAFile), p(*dir, pki.CAKeyFile), p(*dir, pki.StateFile), p(*dir, pki.CRLFile),
			ca.Cert.NotAfter.Format("2006-01-02"), fingerprint(ca.Cert))
		return nil
	case "issue":
		if len(args) == 0 || (args[0] != pki.KindServer && args[0] != pki.KindClient) {
			return errors.New("usage: pkgops pki issue server|client --dir DIR --name NAME")
		}
		kind := args[0]
		fs := newFlags("pki issue " + kind)
		dir := fs.String("dir", "", "CA directory")
		name := fs.String("name", "", "common name (server: HOST[,SAN...])")
		days := fs.Int("days", 0, "validity in days (default 1825, 5 years)")
		kt := fs.String("key-type", "ecdsa", "ecdsa (P-256) or rsa")
		bits := fs.Int("rsa-bits", 3072, "RSA key size")
		outDir := fs.String("out", "", "output directory (default DIR/servers or DIR/clients)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *name == "" {
			return errors.New("--dir and --name are required")
		}
		ca, err := pki.Open(*dir)
		if err != nil {
			return err
		}
		names := strings.Split(*name, ",")
		o := pki.IssueOptions{Kind: kind, Name: strings.TrimSpace(names[0]), Days: *days, KeyType: *kt, RSABits: *bits, OutDir: *outDir}
		if kind == pki.KindServer {
			o.SANs = names[1:]
		} else if len(names) > 1 {
			return errors.New("a client certificate has one name (the machine)")
		}
		is, err := ca.Issue(o)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s certificate for %q, serial %d, valid until %s\n  certificate: %s\n  private key: %s (mode 0400; shown nowhere, copy it to the machine only)\n",
			kind, is.Entry.Name, is.Entry.Serial, is.Entry.NotAfter.Format("2006-01-02"), is.CertFile, is.KeyFile)
		if left := time.Until(ca.Cert.NotAfter); left < 2*365*24*time.Hour {
			fmt.Fprintf(errw, "warning: the CA expires on %s\n", ca.Cert.NotAfter.Format("2006-01-02"))
		}
		return nil
	case "revoke":
		fs := newFlags("pki revoke")
		dir := fs.String("dir", "", "CA directory")
		name := fs.String("name", "", "machine name (revokes its active client certificates)")
		serial := fs.String("serial", "", "serial number (decimal or 0x hex)")
		days := fs.Int("crl-days", pki.DefaultCRLDays, "validity of the regenerated CRL in days")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *dir == "" || (*name == "") == (*serial == "") {
			return errors.New("--dir and exactly one of --name or --serial are required")
		}
		var sn int64
		if *serial != "" {
			var err error
			if sn, err = pki.ParseSerial(*serial); err != nil {
				return err
			}
		}
		ca, err := pki.Open(*dir)
		if err != nil {
			return err
		}
		done, err := ca.Revoke(*name, sn)
		if err != nil {
			return err
		}
		next, err := ca.WriteCRL(*days)
		if err != nil {
			return err
		}
		for _, e := range done {
			fmt.Fprintf(out, "revoked %s %q serial %d\n", e.Kind, e.Name, e.Serial)
		}
		fmt.Fprintf(out, "CRL %s regenerated, next update %s\ncopy it to the server; pkgops serve picks it up within seconds, nginx needs a reload\n", p(*dir, pki.CRLFile), next.Format("2006-01-02"))
		return nil
	case "list":
		fs := newFlags("pki list")
		dir := fs.String("dir", "", "CA directory")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *dir == "" {
			return errors.New("--dir is required")
		}
		ca, err := pki.OpenReadOnly(*dir)
		if err != nil {
			return err
		}
		now := time.Now()
		fmt.Fprintf(out, "CA %q valid until %s\n", ca.Cert.Subject.CommonName, ca.Cert.NotAfter.Format("2006-01-02"))
		fmt.Fprintf(out, "%-7s %-7s %-24s %-12s %s\n", "SERIAL", "KIND", "NAME", "EXPIRES", "STATUS")
		for _, e := range ca.State.Issued {
			st := e.Status(now)
			if st == "active" && e.NotAfter.Sub(now) < 30*24*time.Hour {
				st = "active, expires soon"
			}
			if e.Revoked != nil {
				st = "revoked " + e.Revoked.Format("2006-01-02")
			}
			fmt.Fprintf(out, "%-7d %-7s %-24s %-12s %s\n", e.Serial, e.Kind, e.Name, e.NotAfter.Format("2006-01-02"), st)
		}
		crlLine(out, errw, ca)
		return nil
	case "crl":
		fs := newFlags("pki crl")
		dir := fs.String("dir", "", "CA directory")
		days := fs.Int("days", pki.DefaultCRLDays, "validity (nextUpdate) in days")
		check := fs.Bool("check", false, "only check crl.pem; exit 1 when it is expired or near expiry (for cron)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *dir == "" {
			return errors.New("--dir is required")
		}
		if *check {
			ca, err := pki.OpenReadOnly(*dir)
			if err != nil {
				return err
			}
			if !crlLine(out, errw, ca) {
				return errors.New("CRL needs to be regenerated: pkgops pki crl --dir " + *dir)
			}
			return nil
		}
		ca, err := pki.Open(*dir)
		if err != nil {
			return err
		}
		next, err := ca.WriteCRL(*days)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "CRL %s written, next update %s\n", p(*dir, pki.CRLFile), next.Format("2006-01-02 15:04Z07:00"))
		if time.Until(next) < pki.CRLWarnBefore {
			fmt.Fprintf(errw, "warning: this CRL expires in less than %d days; servers deny all clients after nextUpdate plus the grace\n", int(pki.CRLWarnBefore.Hours()/24))
		}
		return nil
	case "show":
		if len(args) != 1 {
			return errors.New("usage: pkgops pki show FILE")
		}
		return pkiShow(out, args[0])
	}
	fmt.Fprint(errw, pkiUsage)
	return fmt.Errorf("pki: unknown subcommand %q", sub)
}

func p(dir, f string) string { return strings.TrimRight(dir, "/") + "/" + f }

// crlLine prints the CRL status and warns; it returns false when the CRL is
// missing, expired or near expiry.
func crlLine(out, errw io.Writer, ca *pki.CA) bool {
	crl, err := ca.CRLStatus()
	if err != nil {
		fmt.Fprintf(errw, "warning: CRL: %v (run: pkgops pki crl --dir %s)\n", err, ca.Dir)
		return false
	}
	left := time.Until(crl.NextUpdate)
	fmt.Fprintf(out, "CRL number %s, %d revoked, next update %s\n", crl.Number, len(crl.RevokedCertificateEntries), crl.NextUpdate.Format("2006-01-02 15:04Z07:00"))
	switch {
	case left <= 0:
		fmt.Fprintln(errw, "warning: the CRL has EXPIRED: servers deny every client after the grace period; run pkgops pki crl")
		return false
	case left < pki.CRLWarnBefore:
		fmt.Fprintf(errw, "warning: the CRL expires in %d day(s); run pkgops pki crl and copy it to the servers\n", int(left.Hours()/24))
		return false
	}
	return true
}

func fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}

func keyDesc(pub any) string {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return "ECDSA " + k.Curve.Params().Name
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", k.N.BitLen())
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return fmt.Sprintf("%T", pub)
}

var ekuNames = map[x509.ExtKeyUsage]string{x509.ExtKeyUsageServerAuth: "serverAuth", x509.ExtKeyUsageClientAuth: "clientAuth"}

// pkiShow prints certificates and CRLs. Private keys are recognised and never printed.
func pkiShow(out io.Writer, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	n := 0
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		n++
		switch {
		case b.Type == "CERTIFICATE":
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "certificate\n  subject:  %s\n  issuer:   %s\n  serial:   %s\n  valid:    %s to %s\n  key:      %s\n  sha256:   %s\n",
				c.Subject, c.Issuer, c.SerialNumber, c.NotBefore.Format("2006-01-02"), c.NotAfter.Format("2006-01-02"), keyDesc(c.PublicKey), fingerprint(c))
			if c.IsCA {
				fmt.Fprintln(out, "  CA:       yes")
			}
			var eku []string
			for _, e := range c.ExtKeyUsage {
				eku = append(eku, ekuNames[e])
			}
			if len(eku) > 0 {
				fmt.Fprintf(out, "  usage:    %s\n", strings.Join(eku, ", "))
			}
			var sans []string
			sans = append(sans, c.DNSNames...)
			for _, ip := range c.IPAddresses {
				sans = append(sans, ip.String())
			}
			if len(sans) > 0 {
				fmt.Fprintf(out, "  names:    %s\n", strings.Join(sans, ", "))
			}
			if left := time.Until(c.NotAfter); left < 0 {
				fmt.Fprintln(out, "  status:   EXPIRED")
			} else {
				fmt.Fprintf(out, "  status:   valid, %d days left\n", int(left.Hours()/24))
			}
		case b.Type == "X509 CRL":
			rl, err := x509.ParseRevocationList(b.Bytes)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "CRL\n  issuer:      %s\n  number:      %s\n  this update: %s\n  next update: %s\n  revoked:     %d\n",
				rl.Issuer, rl.Number, rl.ThisUpdate.Format(time.RFC3339), rl.NextUpdate.Format(time.RFC3339), len(rl.RevokedCertificateEntries))
			for _, e := range rl.RevokedCertificateEntries {
				fmt.Fprintf(out, "    serial %s revoked %s\n", e.SerialNumber, e.RevocationTime.Format("2006-01-02"))
			}
			if time.Now().After(rl.NextUpdate) {
				fmt.Fprintln(out, "  status:      EXPIRED")
			}
		case strings.Contains(b.Type, "PRIVATE KEY"):
			fmt.Fprintf(out, "%s: private key, not shown\n", b.Type)
		default:
			fmt.Fprintf(out, "%s block\n", b.Type)
		}
	}
	if n == 0 {
		return fmt.Errorf("%s: no PEM data", file)
	}
	return nil
}
