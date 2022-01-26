package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/thepaul/autocert"
	"github.com/zeebo/errs"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

const (
	Version = "0.0.1"
)

var (
	httpListenAddr     = flag.String("http-listen", ":80", "Address to listen on for HTTP requests to web UI and incoming Gerrit events. If empty, don't listen for HTTP.")
	httpsListenAddr    = flag.String("https-listen", ":443", "Address to listen on for HTTPS requests to web UI and incoming Gerrit events. If empty, don't listen for HTTPS.")
	persistentDBSource = flag.String("persistent-db", "sqlite:./persistent.db", "Data source for persistent DB (supported types: sqlite, postgres)")
	teamFile           = flag.String("team-file", "teams.dat", "Where to store information about registered teams")
	externalURL        = flag.String("external-url", "https://localhost.localdomain/", "The URL by which external hosts (including Slack servers) can contact this server")
	operatorEmail      = flag.String("operator-email", "", "Contact email address to be submitted to ACME server (e.g. Let's Encrypt) to be put in issued SSL certificates")
	certRenewBefore    = flag.Duration("cert-renew-before", time.Hour*24*30, "How early certificates should be renewed before they expire")
	certCacheDir       = flag.String("cert-cache-dir", "./ssl-cert-cache/", "A directory on the local filesystem which will be used for storing SSL certificate information. If it does not exist, the directory will be created with 0700 permissions.")
)

func main() {
	flag.Parse()

	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Can't initialize zap logger: %v", err)
	}
	defer func() { panic(logger.Sync()) }()
	errg, ctx := errgroup.WithContext(context.Background())

	governor, err := NewGovernor(ctx, logger, *teamFile)
	if err != nil {
		logger.Fatal("could not set up governor", zap.Error(err))
	}

	parsedURL, err := url.Parse(*externalURL)
	if err != nil {
		logger.Fatal("parsing external-url", zap.String("external-url", *externalURL), zap.Error(err))
	}
	if parsedURL.Scheme != "https" {
		logger.Fatal("invalid external-url: scheme must be https.")
	}
	if parsedURL.Port() != "" {
		logger.Fatal("invalid external-url: port may not be specified. ACME challenges won't work if external hosts can't contact this server on port 443.")
	}
	webState := newUIWebState(logger.Named("web-state"), governor, parsedURL)

	if *httpListenAddr != "" {
		webHandler := newUIWebHandler(logger.Named("web-handler"), webState, false)
		httpServer := newUIWebServer(webState, webHandler)
		httpListener, err := net.Listen("tcp", *httpListenAddr)
		if err != nil {
			logger.Fatal("listening for http", zap.String("listen-addr", *httpListenAddr), zap.Error(err))
		}
		errg.Go(func() error {
			return httpServer.Serve(ctx, httpListener)
		})
	}

	if *httpsListenAddr != "" {
		manager := autocert.NewTLSAutoCertManager(func(ctx context.Context, hostName string) error {
			if hostName != parsedURL.Host {
				return errs.New("invalid hostname %q", hostName)
			}
			return nil
		}, *operatorEmail, *certRenewBefore, *certCacheDir)
		webHandler := newUIWebHandler(logger.Named("web-handler"), webState, true)
		httpsServer := newUIWebServer(webState, webHandler)
		httpsListener, err := manager.Listen("tcp", *httpsListenAddr)
		if err != nil {
			logger.Fatal("listening for https", zap.String("listen-addr", *httpsListenAddr), zap.Error(err))
		}
		errg.Go(func() error {
			return httpsServer.Serve(ctx, httpsListener)
		})
	}

	if err := errg.Wait(); err != nil {
		logger.Fatal("exiting", zap.Error(err))
	}
}
