package slack

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/slackutilsx"
	"github.com/zeebo/errs"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/jtolds/changesetchihuahua/messages"
)

var (
	ClientID      = flag.String("slack-client-id", "13639549360.893676519079", "ID issued to this app by Slack")
	ClientSecret  = flag.String("slack-client-secret", "", "Client secret issued to this app by Slack")
	SigningSecret = flag.String("slack-signing-secret", "", "Signing secret issued to this app by Slack")
	debugSlackLib = flag.Bool("debug-slack-lib", false, "Log debug information from Slack client library")
)

const (
	AddToSlackButton = `<a href="%s"><img alt="Add to Slack" height="40" width="139" src="https://platform.slack-edge.com/img/add_to_slack.png" srcset="https://platform.slack-edge.com/img/add_to_slack.png 1x, https://platform.slack-edge.com/img/add_to_slack@2x.png 2x" /></a>`

	slackAuthURL = "https://slack.com/oauth/v2/authorize"
)

var appScopes = []string{
	"chat:write",
	"dnd:read",
	"im:history",
	"im:read",
	"im:write",
	"links:read",
	"mpim:history",
	"reactions:write",
	"team:read",
	"users:read",
	"users:read.email",
}

var userScopes = []string{}

type BadEvent struct {
	problem string
}

func (e *BadEvent) Error() string {
	return e.problem
}

var VerifyError = errors.New("verify error")

type slackInterface struct {
	api *slack.Client

	rootLogger *zap.Logger
	logger     *zap.Logger

	bot       slack.UserDetails
	team      slack.Team
	oauthData slack.OAuthV2Response

	incomingMessageCallback func(userID, chanID string, isDM bool, text string) string
}

type logWrapper struct {
	*zap.Logger
}

// log all github.com/slack-go/slack messages at Debug level
func (lw logWrapper) Output(callDepth int, s string) error {
	lw.WithOptions(zap.AddCallerSkip(callDepth)).Debug(s)
	return nil
}

type ChatEvent struct {
	slackEvent *slackevents.EventsAPIEvent
}

type EventedChatSystem interface {
	messages.ChatSystem

	HandleEvent(ctx context.Context, event ChatEvent) error
}

func NewSlackInterface(logger *zap.Logger, setupData string) (EventedChatSystem, error) {
	var oauthData slack.OAuthV2Response
	if err := json.Unmarshal([]byte(setupData), &oauthData); err != nil {
		return nil, err
	}

	slackLogger := logWrapper{logger}
	slackOptions := []slack.Option{slack.OptionLog(slackLogger)}
	if *debugSlackLib {
		slackOptions = append(slackOptions, slack.OptionDebug(true))
	}
	slackAPI := slack.New(oauthData.AccessToken, slackOptions...)

	s := &slackInterface{
		api:        slackAPI,
		oauthData:  oauthData,
		rootLogger: logger,
		logger:     logger,
	}
	return s, nil
}

func (s *slackInterface) SetIncomingMessageCallback(cb func(userID, chanID string, isDM bool, text string) string) {
	s.incomingMessageCallback = cb
}

func (s *slackInterface) UnmarshalMessageHandle(handleJSON string) (messages.MessageHandle, error) {
	var mh messageHandle
	if err := json.Unmarshal([]byte(handleJSON), &mh); err != nil {
		return nil, err
	}
	return &mh, nil
}

// StopTeam is returned by HandleEvent when the app has been uninstalled from that team.
var StopTeam = errors.New("stop this team")

func (s *slackInterface) HandleEvent(ctx context.Context, event ChatEvent) (err error) {
	s.logger.Debug("received slack event", zap.String("event-type", event.slackEvent.Type))
	switch event.slackEvent.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.slackEvent.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			return s.handleMessage(ctx, ev)
		case *slackevents.AppUninstalledEvent:
			return StopTeam
		default:
			s.logger.Debug("inner event type not recognized", zap.String("event-datatype", fmt.Sprintf("%T", innerEvent.Data)))
		}
	default:
		s.logger.Debug("outer event type not recognized", zap.String("event-type", event.slackEvent.Type))
	}
	return nil
}

func HandleNoTeamEvent(ctx context.Context, event ChatEvent) (responseBytes []byte) {
	if event.slackEvent == nil {
		return nil
	}
	switch ev := event.slackEvent.Data.(type) {
	case *slackevents.EventsAPIURLVerificationEvent:
		return []byte(ev.Challenge)
	}
	return nil
}

func (s *slackInterface) handleMessage(ctx context.Context, eventData *slackevents.MessageEvent) error {
	if eventData.SubType == "bot_message" {
		// ignore messages from bots, including echoes of messages from this bot itself
		return nil
	}
	// TODO: handle messages in threads, with SubType="message_replied"; replies should go in thread

	s.logger.Debug("received message", zap.Any("message", *eventData))

	if s.incomingMessageCallback != nil {
		reply := s.incomingMessageCallback(eventData.User, eventData.Channel, strings.HasPrefix(eventData.Channel, "D"), eventData.Text)
		if reply != "" {
			_, err := s.PostMessage(ctx, eventData.Channel, reply)
			if err != nil {
				s.logger.Debug("failed to send response to message", zap.Error(err), zap.String("response", reply), zap.Any("message", *eventData))
			}
		}
	}
	return nil
}

func (s *slackInterface) GetInstallingUser(_ context.Context) (string, error) {
	return s.oauthData.AuthedUser.ID, nil
}

func (s *slackInterface) SendNotification(ctx context.Context, id, message string) (messages.MessageHandle, error) {
	// TODO: can the IM channel be cached? is it expected to remain valid as long as the userid?
	params := &slack.OpenConversationParameters{
		Users: []string{id},
	}
	chanID, _, _, err := s.api.OpenConversationContext(ctx, params)
	if err != nil {
		return nil, err
	}
	return s.PostMessage(ctx, chanID.ID, message)
}

func (s *slackInterface) SendPersonalReport(ctx context.Context, chatID, title string, items []string) (messages.MessageHandle, error) {
	return s.SendNotification(ctx, chatID, fmt.Sprintf("*%s*\n%s", title, strings.Join(items, "\n\n")))
}

func (s *slackInterface) SendChannelNotification(ctx context.Context, chanID, message string) (messages.MessageHandle, error) {
	return s.PostMessage(ctx, chanID, message)
}

func (s *slackInterface) SendChannelReport(ctx context.Context, chatID, title string, items []string) (messages.MessageHandle, error) {
	titleHandle, err := s.PostMessage(ctx, chatID, fmt.Sprintf("*%s*", title))
	if err != nil {
		return titleHandle, errs.Wrap(err)
	}
	mh := titleHandle.(*messageHandle)
	_, err = s.PostMessageThread(ctx, chatID, mh.Timestamp, strings.Join(items, "\n\n"))
	return titleHandle, errs.Wrap(err)
}

func (s *slackInterface) PostMessage(ctx context.Context, chanID, message string) (messages.MessageHandle, error) {
	ch, tm, err := s.api.PostMessageContext(ctx, chanID, slack.MsgOptionText(message, false))
	if err != nil {
		return nil, err
	}
	return &messageHandle{Channel: ch, Timestamp: tm}, nil
}

func (s *slackInterface) PostMessageThread(ctx context.Context, chanID, threadTS, message string) (messages.MessageHandle, error) {
	ch, tm, err := s.api.PostMessageContext(ctx, chanID, slack.MsgOptionTS(threadTS), slack.MsgOptionText(message, false))
	if err != nil {
		return nil, err
	}
	return &messageHandle{Channel: ch, Timestamp: tm}, nil
}

func (s *slackInterface) LookupChannelByName(ctx context.Context, channelName string) (string, error) {
	channelName = strings.TrimLeft(channelName, "#")
	cursor := ""
	for {
		conversationsPage, more, err := s.api.GetConversationsForUserContext(ctx, &slack.GetConversationsForUserParameters{
			Cursor:          cursor,
			ExcludeArchived: true,
		})
		if err != nil {
			return "", err
		}
		for _, conversation := range conversationsPage {
			if conversation.Name == channelName || conversation.NameNormalized == channelName {
				return conversation.ID, nil
			}
		}
		if more == "" {
			return "", errs.New("channel %q not found", channelName)
		}
		cursor = more
	}
}

func (s *slackInterface) LookupUserByEmail(ctx context.Context, email string) (messages.ChatUser, error) {
	user, err := s.api.GetUserByEmailContext(ctx, email)
	if err != nil {
		return nil, err
	}
	presence, err := s.GetUserPresence(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return &slackUser{info: user, presence: presence}, nil
}

func (s *slackInterface) GetUserInfoByID(ctx context.Context, chatID string) (messages.ChatUser, error) {
	var eg errgroup.Group
	var user *slack.User
	var presence *slack.UserPresence
	eg.Go(func() (err error) {
		user, err = s.api.GetUserInfoContext(ctx, chatID)
		return err
	})
	eg.Go(func() (err error) {
		presence, err = s.GetUserPresence(ctx, chatID)
		return err
	})
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return &slackUser{info: user, presence: presence}, nil
}

func (s *slackInterface) GetUserPresence(ctx context.Context, chatID string) (*slack.UserPresence, error) {
	return s.api.GetUserPresenceContext(ctx, chatID)
}

func (s *slackInterface) InformBuildStarted(ctx context.Context, mh messages.MessageHandle, link string) error {
	mhObj, ok := mh.(*messageHandle)
	if !ok {
		return errs.New("given message handle is a %T, not a *messageHandle", mh)
	}
	// ignore errors here; usually these won't be present
	_ = s.api.RemoveReactionContext(ctx, "white_check_mark", slack.NewRefToMessage(mhObj.Channel, mhObj.Timestamp))
	_ = s.api.RemoveReactionContext(ctx, "x", slack.NewRefToMessage(mhObj.Channel, mhObj.Timestamp))
	return nil
}

func (s *slackInterface) InformBuildSuccess(ctx context.Context, mh messages.MessageHandle, link string) error {
	mhObj, ok := mh.(*messageHandle)
	if !ok {
		return errs.New("given message handle is a %T, not a *messageHandle", mh)
	}
	return s.api.AddReactionContext(ctx, "white_check_mark", slack.NewRefToMessage(mhObj.Channel, mhObj.Timestamp))
}

func (s *slackInterface) InformBuildFailure(ctx context.Context, mh messages.MessageHandle, link string) error {
	mhObj, ok := mh.(*messageHandle)
	if !ok {
		return errs.New("given message handle is a %T, not a *messageHandle", mh)
	}
	_, _, err := s.api.PostMessageContext(ctx, mhObj.Channel, slack.MsgOptionText("Build failure: "+link, false), slack.MsgOptionTS(mhObj.Timestamp))
	reactionErr := s.api.AddReactionContext(ctx, "x", slack.NewRefToMessage(mhObj.Channel, mhObj.Timestamp))
	return errs.Combine(err, reactionErr)
}

func (s *slackInterface) InformBuildAborted(ctx context.Context, mh messages.MessageHandle, link string) error {
	mhObj, ok := mh.(*messageHandle)
	if !ok {
		return errs.New("given message handle is a %T, not a *messageHandle", mh)
	}
	return s.api.AddReactionContext(ctx, "no_entry_sign", slack.NewRefToMessage(mhObj.Channel, mhObj.Timestamp))
}

func GetOAuthV2Token(ctx context.Context, clientID, clientSecret, code, redirectURI string) (resp *slack.OAuthV2Response, err error) {
	return slack.GetOAuthV2ResponseContext(ctx, http.DefaultClient, clientID, clientSecret, code, redirectURI)
}

// postForm is very similar to slack.postForm(); reimplemented for the sake of getOAuthToken().
func postForm(ctx context.Context, endpoint string, values url.Values, intf interface{}) error {
	reqBody := strings.NewReader(values.Encode())
	req, err := http.NewRequest("POST", endpoint, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errs.New("unexpected status code %d from Slack", resp.StatusCode)
	}
	parser := json.NewDecoder(resp.Body)
	if err := parser.Decode(intf); err != nil {
		return err
	}
	return nil
}

type OAuthV2ResponseTeam struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type OAuthV2ResponseUser struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

type OAuthV2Response struct {
	AccessToken string              `json:"access_token"`
	TokenType   string              `json:"token_type"`
	Scope       string              `json:"scope"`
	BotUserID   string              `json:"bot_user_id"`
	AppID       string              `json:"app_id"`
	Team        OAuthV2ResponseTeam `json:"team"`
	Enterprise  OAuthV2ResponseTeam `json:"enterprise"`
	AuthedUser  OAuthV2ResponseUser `json:"authed_user"`
	slack.SlackResponse
}

func escapeText(t string) string {
	return slackutilsx.EscapeMessage(t)
}

type Formatter struct{}

func (f *Formatter) FormatBold(msg string) string {
	return "*" + msg + "*"
}

func (f *Formatter) FormatItalic(msg string) string {
	return "_" + msg + "_"
}

func (f *Formatter) FormatBlockQuote(msg string) string {
	lines := strings.Split(msg, "\n")
	return "> " + strings.Join(lines, "\n> ")
}

func (f *Formatter) FormatChangeLink(project string, number int, url, subject string) string {
	return fmt.Sprintf("[%s@%d] %s", escapeText(project), number, f.FormatLink(url, subject))
}

func (f *Formatter) FormatUserLink(chatID string) string {
	return fmt.Sprintf("<@%s>", chatID)
}

func (f *Formatter) FormatChannelLink(channelID string) string {
	return fmt.Sprintf("<#%s>", channelID)
}

func (f *Formatter) FormatLink(url, text string) string {
	return fmt.Sprintf("<%s|%s>", url, escapeText(text))
}

func (f *Formatter) UnwrapUserLink(userLink string) string {
	if len(userLink) > 3 && userLink[0] == '<' && userLink[1] == '@' && userLink[len(userLink)-1] == '>' {
		return userLink[2 : len(userLink)-1]
	}
	return ""
}

func (f *Formatter) UnwrapChannelLink(channelLink string) string {
	if len(channelLink) > 3 && channelLink[0] == '<' && channelLink[1] == '#' && channelLink[len(channelLink)-1] == '>' {
		channelLink = channelLink[2 : len(channelLink)-1]
		if pos := strings.Index(channelLink, "|"); pos >= 0 {
			channelLink = channelLink[0:pos]
		}
		return channelLink
	}
	return ""
}

func (f *Formatter) UnwrapLink(link string) string {
	if link[0] == '<' && link[len(link)-1] == '>' {
		link = link[1 : len(link)-1]
		if pos := strings.Index(link, "|"); pos >= 0 {
			link = link[0:pos]
		}
	}
	return link
}

// messageHandle provides a handle to a Slack message, which can be used to change
// or delete that message later.
type messageHandle struct {
	Channel   string
	Timestamp string
}

func (mh *messageHandle) SentTime() time.Time {
	parts := strings.SplitN(mh.Timestamp, ".", 2)
	sec, _ := strconv.ParseInt(parts[0], 10, 64)
	nano := int64(0)
	if len(parts) > 1 {
		for len(parts[1]) < 9 { // haha this is dumb
			parts[1] += "0"
		}
		nano, _ = strconv.ParseInt(parts[1][:9], 10, 64)
	}
	return time.Unix(sec, nano)
}

func (mh *messageHandle) MarshalJSON() ([]byte, error) {
	return json.Marshal(*mh)
}

type slackUser struct {
	info     *slack.User
	presence *slack.UserPresence
}

func (u *slackUser) ChatID() string {
	return u.info.ID
}

func (u *slackUser) RealName() string {
	return u.info.Profile.RealName
}

func (u *slackUser) IsOnline() bool {
	return u.presence.Presence == "active"
}

func (u *slackUser) Timezone() *time.Location {
	return time.FixedZone(fmt.Sprintf("offset%d", u.info.TZOffset), u.info.TZOffset)
}

func AssembleSlackAuthURL(redirectURL string) string {
	values := make(url.Values)
	values.Set("client_id", *ClientID)
	values.Set("scope", strings.Join(appScopes, ","))
	values.Set("user_scope", strings.Join(userScopes, ","))
	values.Set("redirect_uri", redirectURL)
	return slackAuthURL + "?" + values.Encode()
}

func VerifyEventMessage(header http.Header, messageBody []byte) (ev ChatEvent, teamID string, err error) {
	sv, err := slack.NewSecretsVerifier(header, *SigningSecret)
	if err != nil {
		return ev, "", &BadEvent{err.Error()}
	}
	if _, err = sv.Write(messageBody); err != nil {
		return ev, "", err
	}
	if err := sv.Ensure(); err != nil {
		return ev, "", VerifyError
	}
	apiEvent, err := slackevents.ParseEvent(json.RawMessage(messageBody), slackevents.OptionNoVerifyToken())
	if err != nil {
		return ev, "", err
	}
	ev.slackEvent = &apiEvent
	return ev, apiEvent.TeamID, nil
}
