package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/storj/changesetchihuahua/gerrit/events"
	"github.com/storj/changesetchihuahua/slack"
)

var (
	logGerritEvents = flag.Bool("log-gerrit-events", false, "If given, log all Gerrit events received")
)

type uiWebState struct {
	logger      *zap.Logger
	governor    *Governor
	externalURL *url.URL
}

type uiWebServer struct {
	state   *uiWebState
	handler http.Handler
}

func newUIWebState(logger *zap.Logger, governor *Governor, externalURL *url.URL) *uiWebState {
	return &uiWebState{
		logger:      logger,
		governor:    governor,
		externalURL: externalURL,
	}
}

func (ws *uiWebState) slackRedirectURL() string {
	redirectURL := *ws.externalURL
	redirectURL.Path = path.Join(redirectURL.Path, "slack", "oauth")
	return redirectURL.String()
}

func (ws *uiWebState) Setup(w http.ResponseWriter, _ *http.Request) {
	html := fmt.Sprintf(slack.AddToSlackButton, slack.AssembleSlackAuthURL(ws.slackRedirectURL()))
	header := w.Header()
	header.Add("Content-Length", strconv.Itoa(len(html)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func (ws *uiWebState) HandleChatEvent(w http.ResponseWriter, r *http.Request) {
	limited := io.LimitedReader{R: r.Body, N: events.MaxEventPayloadSize}
	body, err := io.ReadAll(&limited)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	responseBytes, err := ws.governor.VerifyAndHandleChatEvent(r.Header, body)
	if err != nil {
		logger := ws.logger.With(zap.Error(err), zap.Any("headers", r.Header), zap.ByteString("event-body", body))
		var badEvent *slack.BadEvent
		if errors.As(err, &badEvent) {
			logger.Info("Bad chat event received")
			w.WriteHeader(http.StatusBadRequest)
		} else if errors.Is(err, slack.ErrVerifyFailed) {
			logger.Warn("Could not verify event")
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			logger.Error("Failed to handle chat event")
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	if responseBytes != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(responseBytes)))
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(responseBytes)
		if err != nil {
			ws.logger.Debug("failed to write response to ResponseWriter", zap.Error(err))
		}
	}
}

func (ws *uiWebState) maybeOAuthRedirect(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	code := values.Get("code")
	if code == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	resp, err := slack.GetOAuthV2Token(r.Context(), *slack.ClientID, *slack.ClientSecret, code, ws.slackRedirectURL())
	if err != nil {
		ws.logger.Error("Failed to acquire OAuth token from Slack", zap.Error(err))
		_, _ = w.Write([]byte(fmt.Sprintf("Failed to acquire OAuth token from Slack: %v", err)))
		return
	}
	ws.logger.Info("OAuth flow success", zap.Any("response", resp))
	jsonBlob, err := json.Marshal(resp)
	if err != nil {
		ws.logger.Error("failed to marshal OAuth token data?!", zap.Error(err))
		_, _ = w.Write([]byte("Failed to record OAuth token data."))
		return
	}
	if err := ws.governor.NewTeam(resp.Team.ID, string(jsonBlob)); err != nil {
		ws.logger.Error("failed to create new team record", zap.Error(err))
		_, _ = w.Write([]byte("Failed to record new team."))
		return
	}

	_, _ = w.Write([]byte(fmt.Sprintf("Success! Changeset Chihuahua is installed to your %s workspace.\n", resp.Team.Name)))
}

func (ws *uiWebState) gerritEvent(w http.ResponseWriter, r *http.Request) {
	urlPath := strings.TrimPrefix(r.URL.Path, "/gerrit/")
	pathParts := strings.SplitN(urlPath, "/", 2)
	teamID := pathParts[0]

	limited := io.LimitedReader{R: r.Body, N: events.MaxEventPayloadSize}
	body, err := io.ReadAll(&limited)
	if err != nil {
		ws.logger.Error("reading gerrit payload", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if *logGerritEvents {
		ws.logger.Debug("Gerrit event received", zap.ByteString("body", body))
	}

	event, err := events.DecodeGerritEvent(body)
	if err != nil {
		ws.logger.Error("decoding payload", zap.Error(err), zap.ByteString("data", body))
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	ws.logger.Debug("received gerrit event", zap.String("origin", r.RemoteAddr), zap.String("event-type", event.GetType()))
	ws.governor.GerritEventReceived(teamID, event)
	w.WriteHeader(http.StatusOK)
}

func newUIWebHandler(logger *zap.Logger, state *uiWebState, isSecure bool) http.Handler {
	mux := NewLoggingMux(logger)
	mux.HandleFunc("/gerrit/", state.gerritEvent)
	if isSecure {
		mux.HandleFunc("/slack/", state.maybeOAuthRedirect)
		mux.HandleFunc("/slack/setup", state.Setup)
		mux.HandleFunc("/slack/events", state.HandleChatEvent)
	}
	return mux
}

func newUIWebServer(state *uiWebState, handler http.Handler) *uiWebServer {
	return &uiWebServer{
		state:   state,
		handler: handler,
	}
}

func (server *uiWebServer) Serve(ctx context.Context, listener net.Listener) error {
	server.state.logger.Info("Listening for connections", zap.String("bind-address", listener.Addr().String()))
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	return http.Serve(listener, server.handler)
}
