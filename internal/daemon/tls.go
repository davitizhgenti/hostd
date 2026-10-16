package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/davitizhgenti/hostd/internal/tlsca"
)

// openCA loads (or, the first time, makes) the machine's CA and keeps its
// leaf certificate current: it is looked at every hour, for a changed
// address (DHCP, a VPN coming up) or an expiry 30 days away.
func openCA(ctx context.Context, log *slog.Logger, dir string, names []string) (*tlsca.Authority, error) {
	ca, err := tlsca.Open(tlsca.Options{Dir: dir, Names: names})
	if err != nil {
		return nil, err
	}
	report := func() {
		if l := ca.Leaf(); len(l.Skipped) > 0 {
			log.Warn("names left out of the certificate: the CA was made before they were configured (see hostctl tls)", "names", l.Skipped)
		}
	}
	report()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if renewed, err := ca.Refresh(); err != nil {
					log.Error("renewing the API's certificate", "err", err)
				} else if renewed {
					l := ca.Leaf()
					log.Info("renewed the API's certificate", "names", l.Names, "ips", l.IPs, "until", l.NotAfter)
					report()
				}
			}
		}
	}()
	return ca, nil
}
