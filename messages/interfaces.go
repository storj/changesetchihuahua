package messages

import (
	"context"
	"encoding/json"
	"time"
)

// MessageHandle represents a sent message. Some chat systems support editing and deletion of
// sent messages, or adding annotations like threads or reactions, and this lets us refer to
// specific messages.
type MessageHandle interface {
	SentTime() time.Time
	json.Marshaler
}

// ChatSystem abstracts interaction with an instant-message chat system, so that this doesn't
// become dependent on Slack specifically. I believe this interface ought to work for Hipchat
// and IRC, but I'm not familiar with the APIs of many other multi-user chat networks.
type ChatSystem interface {
	SetIncomingMessageCallback(cb func(userID, chanID string, isDM bool, text string) string)
	GetInstallingUser(ctx context.Context) (string, error)

	GetUserInfoByID(ctx context.Context, chatID string) (ChatUser, error)
	LookupUserByEmail(ctx context.Context, email string) (ChatUser, error)
	LookupChannelByName(ctx context.Context, name string) (string, error)

	SendNotification(ctx context.Context, chatID, message string) (MessageHandle, error)
	SendPersonalReport(ctx context.Context, chatID, title string, reportItems []string) (MessageHandle, error)
	SendChannelNotification(ctx context.Context, chanID, message string) (MessageHandle, error)
	SendChannelReport(ctx context.Context, chanID, title string, reportItems []string) (MessageHandle, error)

	InformBuildStarted(ctx context.Context, announcement MessageHandle, link string) error
	InformBuildSuccess(ctx context.Context, announcement MessageHandle, link string) error
	InformBuildFailure(ctx context.Context, announcement MessageHandle, link string) error
	InformBuildAborted(ctx context.Context, announcement MessageHandle, link string) error

	InformBuildTypeTriggered(ctx context.Context, announcement MessageHandle, buildType, link string) error
	InformBuildTypeStarted(ctx context.Context, announcement MessageHandle, buildType, link string) error
	InformBuildTypeFailure(ctx context.Context, announcement MessageHandle, buildType, link string) error
	InformBuildTypeSuccess(ctx context.Context, announcement MessageHandle, buildType, link string) error

	UnmarshalMessageHandle(handleData string) (MessageHandle, error)
}

// ChatSystemFormatter controls how to format messages for a specific ChatSystem.
type ChatSystemFormatter interface {
	FormatBold(msg string) string
	FormatItalic(msg string) string
	FormatBlockQuote(msg string) string
	FormatChangeLink(project string, number int, url, subject string) string
	FormatChannelLink(channelID string) string
	FormatUserLink(chatID string) string
	FormatLink(url, text string) string
	FormatCode(msg string) string
	UnwrapUserLink(userLink string) string
	UnwrapChannelLink(channelLink string) string
	UnwrapLink(link string) string
}

// ChatUser represents profile and presence information about a user on ChatSystem.
type ChatUser interface {
	ChatID() string
	RealName() string
	IsOnline() bool
	Timezone() *time.Location
}
