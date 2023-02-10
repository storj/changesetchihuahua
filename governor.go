package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/zeebo/errs"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/storj/changesetchihuahua/app"
	"github.com/storj/changesetchihuahua/gerrit"
	"github.com/storj/changesetchihuahua/gerrit/events"
	"github.com/storj/changesetchihuahua/slack"
)

var notificationTimeout = flag.Duration("notify-timeout", time.Minute*30, "Maximum amount of time to spend trying to deliver a notification")

// Governor controls the Changeset Chihuahua functionality at a top level. It knows about
// all registered teams.
type Governor struct {
	topContext context.Context
	logger     *zap.Logger

	teamsLock sync.Mutex
	teams     map[string]*Team

	teamFileLock sync.Mutex
	teamFile     string
}

// Team is a Slack team that is registered with Changeset Chihuahua.
type Team struct {
	id        string
	logger    *zap.Logger
	canceler  context.CancelFunc
	teamApp   *app.App
	setupData string
	runError  error
}

type vanillaGerritConnector struct{}

func (v vanillaGerritConnector) OpenGerrit(ctx context.Context, logger *zap.Logger, address string) (gerrit.Client, error) {
	return gerrit.OpenClient(ctx, logger, address)
}

// NewGovernor creates a new Governor.
func NewGovernor(ctx context.Context, logger *zap.Logger, teamFile string) (*Governor, error) {
	teamData, err := readTeamFile(teamFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		teamData = make(map[string]string)
	}
	g := &Governor{
		topContext: ctx,
		logger:     logger,
		teams:      make(map[string]*Team),
		teamFile:   teamFile,
	}
	logger.Info("changeset-chihuahua governor starting up", zap.String("version", Version), zap.Int("num-teams", len(teamData)))

	for teamID, setupData := range teamData {
		if err := g.StartTeam(teamID, setupData); err != nil {
			logger.Error("failed to start team", zap.String("team-id", teamID), zap.Error(err))
		}
	}
	return g, nil
}

func readTeamFile(fileName string) (teamData map[string]string, err error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer func() { err = errs.Combine(err, f.Close()) }()

	teamData = make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		teamLine := strings.TrimSpace(scanner.Text())
		if teamLine == "" || strings.HasPrefix(teamLine, "#") {
			continue
		}
		parts := strings.SplitN(teamLine, " ", 2)
		if len(parts) < 2 {
			return nil, errs.New("invalid team definition line in %q", fileName)
		}
		teamData[parts[0]] = parts[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, errs.New("reading from %q: %v", fileName, err)
	}
	return teamData, nil
}

// NewTeam is called when a new Slack team is registered with Changeset Chihuahua. It adds the
// team definition so that we will still have it after a restart, then creates a new Team
// instance and calls Run on it.
func (g *Governor) NewTeam(teamID string, setupData string) error {
	g.teamsLock.Lock()
	defer g.teamsLock.Unlock()

	if _, ok := g.teams[teamID]; ok {
		return errs.New("team %s is already active", teamID)
	}
	if strings.ContainsAny(teamID, " \n") {
		return errs.New("invalid team ID")
	}
	if strings.Contains(setupData, "\n") {
		return errs.New("invalid setup data")
	}
	if err := g.appendTeamDefinition(teamID, setupData); err != nil {
		return errs.New("could not add team definition: %v", err)
	}
	team := &Team{
		id:        teamID,
		setupData: setupData,
		logger:    g.logger.Named(teamID),
	}
	g.teams[teamID] = team
	go team.Run(g.topContext)
	return nil
}

// StartTeam is called at program start for already-registered teams. It creates the
// appropriate Team instance and calls Run on it.
func (g *Governor) StartTeam(teamID, setupData string) error {
	g.teamsLock.Lock()
	defer g.teamsLock.Unlock()

	if _, ok := g.teams[teamID]; ok {
		return errs.New("team %s is already active", teamID)
	}
	team := &Team{
		id:        teamID,
		setupData: setupData,
		logger:    g.logger.Named(teamID),
	}
	g.teams[teamID] = team
	go team.Run(g.topContext)
	return nil
}

// Run takes care of all per-team functionality. It creates a Slack client for the team,
// manages the database for team config and events, and arranges for periodic Gerrit
// reports.
func (t *Team) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer func() {
		if t.runError != nil {
			t.logger.Error("Failed to run", zap.Error(t.runError))
		}
	}()

	slackClient, err := slack.NewSlackInterface(t.logger.Named("chat"), t.setupData)
	if err != nil {
		t.runError = errs.New("could not initialize slack connection: %v", err)
		return
	}
	teamDBSource, err := addSearchPath(*persistentDBSource, "team-"+t.id)
	if err != nil {
		t.runError = errs.New("could not parse %q: %v", *persistentDBSource, err)
		return
	}
	persistentDB, err := app.NewPersistentDB(t.logger.Named("db"), teamDBSource)
	if err != nil {
		t.runError = errs.New("could not open db: %v", err)
		return
	}
	t.teamApp = app.New(ctx, t.logger, slackClient, &slack.Formatter{}, persistentDB, vanillaGerritConnector{})

	var errGroup errgroup.Group
	errGroup.Go(func() error {
		return t.teamApp.PeriodicTeamReports(ctx, time.Now)
	})
	errGroup.Go(func() error {
		return t.teamApp.PeriodicPersonalReports(ctx, time.Now)
	})
	err = errGroup.Wait()
	t.logger.Info("Team errgroup exited", zap.String("team-id", t.id), zap.Error(err))
	err = t.Close()
	if err != nil {
		t.logger.Error("failed to close team", zap.Error(err))
	}
}

// GerritEventReceived is called when an event is received from Gerrit. The Governor determines
// the appropriate Team and passes the event on to it.
func (g *Governor) GerritEventReceived(teamID string, event events.GerritEvent) {
	g.teamsLock.Lock()
	team, ok := g.teams[teamID]
	g.teamsLock.Unlock()
	if !ok {
		g.logger.Info("received event for unknown team", zap.String("team-id", teamID))
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(g.topContext, *notificationTimeout)
		defer cancel()

		team.teamApp.GerritEvent(ctx, event)
	}()
}

// VerifyAndHandleChatEvent is called when an HTTP request is received which purports to be
// from Slack. The request is verified, and if valid, is passed on to the appropriate Team.
func (g *Governor) VerifyAndHandleChatEvent(header http.Header, messageBody []byte) (responseBytes []byte, err error) {
	event, teamID, err := slack.VerifyEventMessage(header, messageBody)
	if err != nil {
		return nil, err
	}
	g.teamsLock.Lock()
	team, ok := g.teams[teamID]
	g.teamsLock.Unlock()
	if !ok {
		g.logger.Info("received chat event for unknown team", zap.String("team-id", teamID), zap.Any("event", event))
		responseBytes = slack.HandleNoTeamEvent(g.topContext, event)
		return responseBytes, nil
	}

	go func() {
		err := team.teamApp.ChatEvent(g.topContext, event)
		if errors.Is(err, slack.ErrStopTeam) {
			g.logger.Info("uninstalled from team", zap.String("team-id", teamID))
			g.teamsLock.Lock()
			delete(g.teams, teamID)
			g.teamsLock.Unlock()

			if err := team.teamApp.Close(); err != nil {
				g.logger.Info("failed to close team", zap.String("team-id", teamID), zap.Error(err))
			}
		} else {
			g.logger.Error("Unexpected error from teamApp.ChatEvent", zap.Error(err))
		}
	}()
	return nil, nil
}

func (g *Governor) appendTeamDefinition(teamID, setupData string) (err error) {
	g.teamFileLock.Lock()
	defer g.teamFileLock.Unlock()

	f, err := os.OpenFile(g.teamFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer func() { err = errs.Combine(err, f.Close()) }()

	dataLine := teamID + " " + setupData + "\n"
	if _, err := f.Write([]byte(dataLine)); err != nil {
		return err
	}
	return nil
}

// Close shuts down functionality for a Team.
func (t *Team) Close() error {
	t.canceler()
	return t.teamApp.Close()
}

func addSearchPath(dbURL, schemaName string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		query := u.Query()
		query.Set("options", "--search_path="+pq.QuoteIdentifier(schemaName))
		u.RawQuery = query.Encode()
	case "sqlite", "sqlite3":
		addSuffix := ""
		if strings.HasSuffix(u.Opaque, ".db") {
			addSuffix = ".db"
			u.Opaque = u.Opaque[:len(u.Opaque)-3]
		}
		u.Opaque += "." + schemaName + addSuffix
	default:
		return "", errs.New("unrecognized db scheme %q", u.Scheme)
	}
	return u.String(), nil
}
