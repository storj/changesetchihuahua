package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/storj/changesetchihuahua/app"
	"github.com/storj/changesetchihuahua/gerrit"
	"github.com/storj/changesetchihuahua/gerrit/events"
	"github.com/storj/changesetchihuahua/messages"
	"github.com/storj/changesetchihuahua/slack"
)

//go:generate mockgen -destination messages_mocks_test.go -package app_test github.com/storj/changesetchihuahua/slack EventedChatSystem
//go:generate mockgen -destination gerrit_mocks_test.go -package app_test github.com/storj/changesetchihuahua/gerrit Client

type testSystem struct {
	T           *testing.T
	Ctx         context.Context
	DB          *app.PersistentDB
	Controller  *gomock.Controller
	MockChat    *MockEventedChatSystem
	App         *app.App
	MockGerrit  *MockClient
	SkipClose   bool
	MockClients map[string]*MockClient
}

func (ts *testSystem) OpenGerrit(_ context.Context, log *zap.Logger, _ string) (gerrit.Client, error) {
	log.Debug("Gerrit fake client ready")
	ts.MockGerrit = NewMockClient(ts.Controller)
	ts.MockGerrit.EXPECT().Close().Times(1).Return(nil)
	return ts.MockGerrit, nil
}

func (ts *testSystem) InjectEvent(eventJSON string) {
	ev, err := events.DecodeGerritEvent([]byte(eventJSON))
	require.NoError(ts.T, err)
	ts.App.GerritEvent(ts.Ctx, ev)
}

func (ts *testSystem) makeUser(email, username, name string) *hypotheticalUser {
	hu := newHypotheticalUser(email, username, name, "", 0)
	ts.MockChat.EXPECT().
		LookupUserByEmail(gomock.Any(), gomock.Eq(email)).
		AnyTimes().
		Return(hu, nil)
	return hu
}

type dummyMessageHandle struct {
	ImID string
	Time time.Time
}

var _ messages.MessageHandle = (*dummyMessageHandle)(nil)

func (d *dummyMessageHandle) SentTime() time.Time {
	return d.Time
}

func (d *dummyMessageHandle) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.ImID + "_" + d.Time.String())
}

func (d *dummyMessageHandle) AsJSON() string {
	s, err := d.MarshalJSON()
	if err != nil {
		panic(err)
	}
	return string(s)
}

type hypotheticalUser struct {
	name     string
	email    string
	username string
	gerritID int
	chatID   string
	isOnline bool
	tz       *time.Location
}

var _ messages.ChatUser = (*hypotheticalUser)(nil)

func newHypotheticalUser(email, username, name, chatID string, gerritID int) *hypotheticalUser {
	if chatID == "" {
		chatID = "CHATID(" + username + ")"
	}
	if gerritID == 0 {
		gerritID = int(100000 + rand.Int31n(10000))
	}
	return &hypotheticalUser{
		name:     name,
		email:    email,
		username: username,
		gerritID: gerritID,
		chatID:   chatID,
	}
}

func (hu *hypotheticalUser) ChatID() string           { return hu.chatID }
func (hu *hypotheticalUser) RealName() string         { return hu.name }
func (hu *hypotheticalUser) IsOnline() bool           { return hu.isOnline }
func (hu *hypotheticalUser) Timezone() *time.Location { return hu.tz }

func (hu *hypotheticalUser) GerritAccount() *gerrit.AccountInfo {
	return &gerrit.AccountInfo{
		AccountID: hu.gerritID,
		Name:      hu.name,
		Email:     hu.email,
		Username:  hu.username,
	}
}

func (hu *hypotheticalUser) JSON() string {
	s, err := json.Marshal(struct {
		Name     string
		Email    string
		Username string
	}{
		Name:     hu.name,
		Email:    hu.email,
		Username: hu.username,
	})
	if err != nil {
		panic(err)
	}
	return string(s)
}

const adminUserID = "fred"

func testWithMockChat(t *testing.T, config map[string]string, testFunc func(*testSystem)) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	logger := zaptest.NewLogger(t)

	db, err := app.NewPersistentDB(logger.Named("db"), "sqlite3:"+filepath.Join(t.TempDir(), "sqlite.db"))
	require.NoError(t, err)

	for key, value := range config {
		err = db.SetConfig(ctx, key, value)
		require.NoError(t, err)
	}

	m := NewMockEventedChatSystem(ctrl)

	m.EXPECT().
		SetIncomingMessageCallback(gomock.Any()).
		Times(1)
	m.EXPECT().
		GetInstallingUser(gomock.Any()).
		Times(1).
		Return(adminUserID, nil)

	ts := &testSystem{
		T:           t,
		Ctx:         ctx,
		DB:          db,
		Controller:  ctrl,
		MockChat:    m,
		MockClients: make(map[string]*MockClient),
	}
	ts.App = app.New(ctx, logger.Named("app"), m, &slack.Formatter{}, db, ts)
	require.NotNil(t, ts.App)

	if gerritAddr, ok := config["gerrit-address"]; ok {
		err := ts.App.ConfigureGerritServer(ctx, gerritAddr)
		require.NoError(t, err)
	}
	testFunc(ts)

	if !ts.SkipClose {
		err = ts.App.Close()
		require.NoError(t, err)
	}
}

func TestCommentAdded(t *testing.T) {
	testWithMockChat(t, map[string]string{
		"gerrit-address":        "https://gerrit.jorts.io",
		"remove-project-prefix": "jorts/",
		"global-notify-channel": "GLOBALNOTIFY",
	}, func(ts *testSystem) {
		hilmac := ts.makeUser("hilmac@jorts.io", "hilmac", "Hilmac Learnwiz")
		navi := ts.makeUser("navi@jorts.io", "navi", "Navi Xerdafies")
		bobson := ts.makeUser("bobson@jorts.io", "bdugnutt", "Bobson Dugnutt")
		robot := ts.makeUser("", "jortsrobot", "Jorts Robot")

		ts.MockGerrit.EXPECT().
			GetChangeEx(gomock.Any(), gomock.Eq("jorts/jorts~810"), gomock.Any()).
			MaxTimes(5).
			Return(gerrit.ChangeInfo{
				ID:                     "jorts%2Fjorts~master~Idb12228ef627760ad9e8fc0bb49a0556a0c166b4",
				Project:                "jorts/jorts",
				Branch:                 "master",
				Assignee:               gerrit.AccountInfo{},
				ChangeID:               "Idb12228ef627760ad9e8fc0bb49a0556a0c166b4",
				Subject:                "short/jeans: reinforce and deepen pockets",
				Status:                 "NEW",
				Created:                "2019-12-17 13:22:19.000000000",
				Updated:                "2020-01-03 10:21:09.000000000",
				Insertions:             84,
				Deletions:              133,
				TotalCommentCount:      54,
				UnresolvedCommentCount: 10,
				Number:                 810,
				Owner:                  *navi.GerritAccount(),
				Messages: []gerrit.ChangeMessageInfo{
					{
						ID:             "a7ba006b2db0d079d5cf82e1b99610b805be5fb4",
						Author:         *navi.GerritAccount(),
						RealAuthor:     navi.GerritAccount(),
						Date:           "2019-12-17 13:22:19.000000000",
						Message:        "Uploaded patch set 1.",
						Tag:            "autogenerated:gerrit:newWipPatchSet",
						RevisionNumber: 1,
					},
					{
						ID:             "6b3a34bd176e5a7e44a4ccb42f4b9f20b2a8faf9",
						Tag:            "autogenerated:gerrit:newWipPatchSet",
						Author:         *navi.GerritAccount(),
						RealAuthor:     navi.GerritAccount(),
						Date:           "2019-12-18 07:20:12.000000000",
						Message:        "Uploaded patch set 2.",
						RevisionNumber: 2,
					},
					{
						ID:             "565f3e3a877c4e819989dafa44ea8b6114c07c68",
						Author:         *navi.GerritAccount(),
						RealAuthor:     navi.GerritAccount(),
						Date:           "2019-12-18 07:23:39.000000000",
						Message:        "Patch Set 2: Code-Review+2",
						RevisionNumber: 2,
					},
					{
						ID:             "ec71deb052ad6cd0fa38659c69f0ea05c9e6f0ef",
						Tag:            "autogenerated:gerrit:deleteVote",
						Author:         *navi.GerritAccount(),
						RealAuthor:     navi.GerritAccount(),
						Date:           "2019-12-18 07:23:51.000000000",
						Message:        "Removed Code-Review+2 by Navi Xerdafies <navi@jorts.io>\n",
						RevisionNumber: 2,
					},
					{
						ID:             "cfe79b2ff9b8cd12b39a72a6fd456c911c391377",
						Tag:            "autogenerated:gerrit:newPatchSet",
						Author:         *navi.GerritAccount(),
						RealAuthor:     navi.GerritAccount(),
						Date:           "2019-12-18 07:28:20.000000000",
						Message:        "Uploaded patch set 3.",
						RevisionNumber: 3,
					},
					{
						ID:             "ccfaf312c85737d096d73226afd6da7c809a721d",
						Tag:            "autogenerated:jenkins-gerrit-trigger",
						Author:         *robot.GerritAccount(),
						RealAuthor:     robot.GerritAccount(),
						Date:           "2019-12-18 07:28:29.000000000",
						Message:        "Patch Set 3:\n\nBuild Started https://build.dev.jorts.io/job/jorts-gerrit/792/",
						RevisionNumber: 3,
					},
					{
						ID:             "5b242503075acf3bf78b25efd681f7078d4ded5b",
						Tag:            "autogenerated:jenkins-gerrit-trigger",
						Author:         *robot.GerritAccount(),
						RealAuthor:     robot.GerritAccount(),
						Date:           "2019-12-18 07:37:45.000000000",
						Message:        "Patch Set 3: Verified-1\n\nBuild Failed \n\nhttps://build.dev.jorts.io/job/jorts-gerrit/792/ : FAILURE",
						RevisionNumber: 3,
					},
					{
						ID:             "b69cb67321dbd8a191ac3957dc98c1056dc1b6c9",
						Author:         *hilmac.GerritAccount(),
						RealAuthor:     hilmac.GerritAccount(),
						Date:           "2020-01-01 03:12:47.000000000",
						Message:        "Patch Set 25:\n\n(1 comment)",
						RevisionNumber: 25,
					},
				},
				HasReviewStarted: true,
			}, nil)
		ts.MockGerrit.EXPECT().
			ListRevisionComments(gomock.Any(), gomock.Eq("jorts/jorts~810"), gomock.Eq("25")).
			Times(1).
			Return(map[string][]gerrit.CommentInfo{
				"denim/denim.go": {
					gerrit.CommentInfo{
						PatchSet:   25,
						ID:         "8a6101cd_0f3fd0da",
						Path:       "denim/denim.go",
						Line:       19,
						InReplyTo:  "",
						Message:    "Is this even possible?",
						Updated:    "2019-12-28 19:23:31.000000000",
						Author:     *bobson.GerritAccount(),
						Unresolved: true,
					},
					gerrit.CommentInfo{
						PatchSet:   25,
						ID:         "b2ab407d_55ba602d",
						Path:       "denim/denim.go",
						Line:       19,
						InReplyTo:  "8a6101cd_0f3fd0da",
						Message:    "Not only is it possible, it is necessary",
						Updated:    "2020-01-01 03:12:48.000000000",
						Author:     *hilmac.GerritAccount(),
						Unresolved: true,
					},
					gerrit.CommentInfo{
						PatchSet:   25,
						ID:         "a4924f69_fb52eb16",
						Path:       "denim/denim.go",
						Line:       48,
						InReplyTo:  "",
						Message:    "Doesn't this violate the No-Cloning Theorem?\n\nI'm no physicist.",
						Updated:    "2020-01-01 03:12:48.000000000",
						Author:     *hilmac.GerritAccount(),
						Unresolved: true,
					},
				},
			}, nil)

		// notification to global notification channel does not link author or include all inline comments
		ts.MockChat.EXPECT().
			SendChannelNotification(gomock.Any(), gomock.Eq("GLOBALNOTIFY"), stringTrimEq("Hilmac Learnwiz <https://gerrit.jorts.io/c/jorts/jorts/+/810#message-b69cb67321dbd8a191ac3957dc98c1056dc1b6c9|commented on> [jorts@810] <https://gerrit.jorts.io/c/jorts/jorts/+/810|short/jeans: reinforce and deepen pockets> patchset 25: _(with <https://gerrit.jorts.io/c/jorts/jorts/+/810#message-b69cb67321dbd8a191ac3957dc98c1056dc1b6c9|2 inline comments>)_")).
			Times(1).
			Return(nil, nil)
		// notification to change owner does include all inline comments
		ts.MockChat.EXPECT().
			SendNotification(gomock.Any(), gomock.Eq("CHATID(navi)"), stringTrimEq("<@CHATID(hilmac)> <https://gerrit.jorts.io/c/jorts/jorts/+/810#message-b69cb67321dbd8a191ac3957dc98c1056dc1b6c9|commented on> [jorts@810] <https://gerrit.jorts.io/c/jorts/jorts/+/810|short/jeans: reinforce and deepen pockets> patchset 25:\n* <https://gerrit.jorts.io/c/jorts/jorts/+/810/25/denim/denim.go#19|denim/denim.go:19>: Not only is it possible, it is necessary\n* <https://gerrit.jorts.io/c/jorts/jorts/+/810/25/denim/denim.go#48|denim/denim.go:48>:\n> Doesn't this violate the No-Cloning Theorem?\n>\n> I'm no physicist.")).
			Times(1).
			Return(nil, nil)
		// notification to prior thread participant includes only relevant inline comment
		ts.MockChat.EXPECT().
			SendNotification(gomock.Any(), gomock.Eq("CHATID(bdugnutt)"), stringTrimEq("<@CHATID(hilmac)> replied to a thread on [jorts@810] <https://gerrit.jorts.io/c/jorts/jorts/+/810|short/jeans: reinforce and deepen pockets>: <https://gerrit.jorts.io/c/jorts/jorts/+/810/25/denim/denim.go#19|denim/denim.go:19>: Not only is it possible, it is necessary")).
			Times(1).
			Return(nil, nil)

		// simulate the incoming Gerrit event that will trigger those calls
		ts.InjectEvent(`{
			"author": ` + hilmac.JSON() + `,
			"approvals": [
				{
					"type": "Verified",
					"description": "Verified",
					"value": "0"
				},
				{
					"type": "Code-Review",
					"description": "Code-Review",
					"value": "0"
				}
			],
			"comment": "Patch Set 25:\n\n(1 comment)",
			"patchSet": {
				"number": 25,
				"revision": "ea97854b98837268d36d6076b76f9a29dd7bc2c3",
				"parents": ["75cd605d5abc5ba7e687a746135ddfbfb8672959"],
				"ref": "refs/changes/10/810/25",
				"uploader": ` + navi.JSON() + `,
				"createdOn": 1577703150,
				"author": ` + navi.JSON() + `,
				"kind": "REWORK",
				"sizeInsertions": 107,
				"sizeDeletions": -133
			},
			"change": {
				"project": "jorts/jorts",
				"branch": "master",
				"id": "Idb12228ef627760ad9e8fc0bb49a0556a0c166b4",
				"number": 810,
				"subject": "short/jeans: reinforce and deepen pockets",
				"owner": ` + navi.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/jorts/+/810",
				"commitMessage": "short/jeans: reinforce and deepen pockets\n\nMore pocket research is required for further development.\n\nChange-Id: Idb12228ef627760ad9e8fc0bb49a0556a0c166b4\n",
				"createdOn": 1576523403,
				"status":"NEW"
			},
			"project": {
				"name": "jorts/jorts"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "Idb12228ef627760ad9e8fc0bb49a0556a0c166b4"
			},
			"type": "comment-added",
			"eventCreatedOn": 1577782831
		}`)
	})
}

func TestJenkinsCommentAdded(t *testing.T) {
	testWithMockChat(t, map[string]string{
		"gerrit-address":           "https://gerrit.jorts.io",
		"remove-project-prefix":    "jorts/",
		"global-notify-channel":    "GLOBALNOTIFY",
		"jenkins-robot-user":       "doty2.0",
		"jenkins-link-transformer": "s,https://build.dev.jorts.io/(.*),https://changedhost/$1,",
	}, func(ts *testSystem) {
		scanlan := ts.makeUser("bard@voxmachina.dd", "meatman", "Scanlan Shorthalt")
		grog := ts.makeUser("grog@voxmachina.dd", "grandpoobah", "Grog Strongjaw")
		robot := ts.makeUser("doty@voxmachina.dd", "doty2.0", "Doty 2.0")
		ts.MockGerrit.EXPECT().
			GetPatchSetInfo(gomock.Any(), "jorts/exandria~928", "2").
			Times(1).
			Return(gerrit.ChangeInfo{
				ID:       "jorts%2Fexandria~master~If2743d74527f838fac6233693e8ac7147bcaad8c",
				Project:  "jorts/exandria",
				Branch:   "master",
				ChangeID: "If2743d74527f838fac6233693e8ac7147bcaad8c",
				Subject:  "Add Lionel as a contributor",
				Status:   "NEW",
				Number:   928,
				Owner:    *grog.GerritAccount(),
			}, nil)

		messageSendTime := time.Now()
		msgHandle1 := &dummyMessageHandle{"GLOBALNOTIFY", messageSendTime}
		msgHandle2 := &dummyMessageHandle{"IMID(grandpoobah)", messageSendTime}

		ts.MockChat.EXPECT().
			SendChannelNotification(gomock.Any(), "GLOBALNOTIFY", "Scanlan Shorthalt uploaded a new patchset #2 on change [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor> (<https://gerrit.jorts.io/c/jorts/exandria/+/928/1..2|see changes>)").
			Times(1).
			Return(msgHandle1, nil)
		ts.MockChat.EXPECT().
			SendNotification(gomock.Any(), grog.chatID, "<@CHATID(meatman)> uploaded a new patchset #2 on your change [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor>").
			Times(1).
			Return(msgHandle2, nil)

		// inform uploader and owner of failed and succeeded builds
		gomock.InOrder(
			ts.MockChat.EXPECT().
				SendNotification(gomock.Any(), scanlan.chatID, "Build for [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor> failed: https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil, nil),
			ts.MockChat.EXPECT().
				SendNotification(gomock.Any(), scanlan.chatID, "Build for [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor> succeeded").
				Times(1).
				Return(nil, nil),
		)
		gomock.InOrder(
			ts.MockChat.EXPECT().
				SendNotification(gomock.Any(), grog.chatID, "Build for [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor> failed: https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil, nil),
			ts.MockChat.EXPECT().
				SendNotification(gomock.Any(), grog.chatID, "Build for [exandria@928] <https://gerrit.jorts.io/c/jorts/exandria/+/928|Add Lionel as a contributor> succeeded").
				Times(1).
				Return(nil, nil),
		)

		// annotate build announcements with build status
		ts.MockChat.EXPECT().
			UnmarshalMessageHandle(msgHandle1.AsJSON()).
			AnyTimes().
			Return(msgHandle1, nil)
		ts.MockChat.EXPECT().
			UnmarshalMessageHandle(msgHandle2.AsJSON()).
			AnyTimes().
			Return(msgHandle2, nil)

		gomock.InOrder(
			ts.MockChat.EXPECT().InformBuildStarted(gomock.Any(), msgHandle1, "https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildFailure(gomock.Any(), msgHandle1, "https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildStarted(gomock.Any(), msgHandle1, "https://changedhost/job/jorts-gerrit/19284/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildSuccess(gomock.Any(), msgHandle1, "https://changedhost/job/jorts-gerrit/19284/").
				Times(1).
				Return(nil),
		)
		gomock.InOrder(
			ts.MockChat.EXPECT().InformBuildStarted(gomock.Any(), msgHandle2, "https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildFailure(gomock.Any(), msgHandle2, "https://changedhost/job/jorts-gerrit/19283/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildStarted(gomock.Any(), msgHandle2, "https://changedhost/job/jorts-gerrit/19284/").
				Times(1).
				Return(nil),
			ts.MockChat.EXPECT().InformBuildSuccess(gomock.Any(), msgHandle2, "https://changedhost/job/jorts-gerrit/19284/").
				Times(1).
				Return(nil),
		)

		// simulate the incoming Gerrit events that will trigger those calls

		// first, the patchset creation
		ts.InjectEvent(`{
			"uploader": ` + scanlan.JSON() + `,
			"patchSet": {
				"number": 2,
				"revision": "d4c577711813d50b40525708b646f7b04cf910f6",
				"parents": ["fae4441a0b3cab1452c597ce2d3605d50993be0e"],
				"ref": "refs/changes/28/928/2",
				"uploader": ` + scanlan.JSON() + `,
				"createdOn": 1496368800,
				"author": ` + scanlan.JSON() + `,
				"kind": "REWORK",
				"sizeInsertions": 192,
				"sizeDeletions": -14
			},
			"change": {
				"project": "jorts/exandria",
				"branch": "master",
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c",
				"number": 928,
				"subject": "Add Lionel as a contributor",
				"owner": ` + grog.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/exandria/+/928",
				"commitMessage": "Add Lionel as a contributor\n\nChange-Id: If2743d74527f838fac6233693e8ac7147bcaad8c\n",
				"status": "NEW"
			},
			"project": {
				"name": "jorts/exandria"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c"
			},
			"type": "patchset-created",
			"eventCreatedOn": 1496368800
		}`)

		// now, the build started message
		ts.InjectEvent(`{
			"author": ` + robot.JSON() + `,
			"comment": "Patch Set 2:\n\nBuild Started https://build.dev.jorts.io/job/jorts-gerrit/19283/",
			"patchSet": {
				"number": 2,
				"revision": "e84efae74ce84f4ae106b6bebff51617ff7d5ccb",
				"uploader": ` + scanlan.JSON() + `,
				"author": ` + scanlan.JSON() + `,
				"kind": "REWORK"
			},
			"change": {
				"project": "jorts/exandria",
				"branch": "master",
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c",
				"number": 928,
				"subject": "Add Lionel as a contributor",
				"owner": ` + grog.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/exandria/+/928",
				"commitMessage": "Add Lionel as a contributor\n\nChange-Id: If2743d74527f838fac6233693e8ac7147bcaad8c\n",
				"status": "NEW"
			},
			"project": {
				"name": "jorts/exandria"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c"
			},
			"type": "comment-added",
			"eventCreatedOn": 1496368804
		}`)

		// a build failed message
		ts.InjectEvent(`{
			"author": ` + robot.JSON() + `,
			"comment": "Patch Set 2: -Verified \n\nBuild Failed\n\nhttps://build.dev.jorts.io/job/jorts-gerrit/19283/ : FAILURE",
			"patchSet": {
				"number": 2,
				"revision": "e84efae74ce84f4ae106b6bebff51617ff7d5ccb",
				"uploader": ` + scanlan.JSON() + `,
				"author": ` + scanlan.JSON() + `,
				"kind": "REWORK"
			},
			"change": {
				"project": "jorts/exandria",
				"branch": "master",
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c",
				"number": 928,
				"subject": "Add Lionel as a contributor",
				"owner": ` + grog.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/exandria/+/928",
				"commitMessage": "Add Lionel as a contributor\n\nChange-Id: If2743d74527f838fac6233693e8ac7147bcaad8c\n",
				"status": "NEW"
			},
			"project": {
				"name": "jorts/exandria"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c"
			},
			"type": "comment-added",
			"eventCreatedOn": 1496369421
		}`)

		// say the user retriggered the build in jenkins, yieldiing a new "build started" message
		ts.InjectEvent(`{
			"author": ` + robot.JSON() + `,
			"comment": "Patch Set 2:\n\nBuild Started https://build.dev.jorts.io/job/jorts-gerrit/19284/",
			"patchSet": {
				"number": 2,
				"revision": "e84efae74ce84f4ae106b6bebff51617ff7d5ccb",
				"uploader": ` + scanlan.JSON() + `,
				"author": ` + scanlan.JSON() + `,
				"kind": "REWORK"
			},
			"change": {
				"project": "jorts/exandria",
				"branch": "master",
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c",
				"number": 928,
				"subject": "Add Lionel as a contributor",
				"owner": ` + grog.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/exandria/+/928",
				"commitMessage": "Add Lionel as a contributor\n\nChange-Id: If2743d74527f838fac6233693e8ac7147bcaad8c\n",
				"status": "NEW"
			},
			"project": {
				"name": "jorts/exandria"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c"
			},
			"type": "comment-added",
			"eventCreatedOn": 1496369216
		}`)

		// and this time it succeeded
		ts.InjectEvent(`{
			"author": ` + robot.JSON() + `,
			"comment": "Patch Set 2: Verified+1\n\nBuild Successful \n\nhttps://build.dev.jorts.io/job/jorts-gerrit/19284/ : SUCCESS",
			"patchSet": {
				"number": 2,
				"revision": "e84efae74ce84f4ae106b6bebff51617ff7d5ccb",
				"uploader": ` + scanlan.JSON() + `,
				"author": ` + scanlan.JSON() + `,
				"kind": "REWORK"
			},
			"change": {
				"project": "jorts/exandria",
				"branch": "master",
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c",
				"number": 928,
				"subject": "Add Lionel as a contributor",
				"owner": ` + grog.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/exandria/+/928",
				"commitMessage": "Add Lionel as a contributor\n\nChange-Id: If2743d74527f838fac6233693e8ac7147bcaad8c\n",
				"status": "NEW"
			},
			"project": {
				"name": "jorts/exandria"
			},
			"refName": "refs/heads/master",
			"changeKey": {
				"id": "If2743d74527f838fac6233693e8ac7147bcaad8c"
			},
			"type": "comment-added",
			"eventCreatedOn": 1496369229
		}`)
	})
}

func TestReviewerAdded(t *testing.T) {
	testWithMockChat(t, map[string]string{
		"gerrit-address":        "https://gerrit.jorts.io",
		"remove-project-prefix": "jorts/",
		"global-notify-channel": "GLOBALNOTIFY",
	}, func(ts *testSystem) {
		userA := ts.makeUser("user-a@jorts.io", "user_a", "Yoozer Eyy")
		userB := ts.makeUser("user-b@jorts.io", "user_b", "")
		userC := ts.makeUser("user-c@jorts.io", "user_c", "You Zersee")
		userD := ts.makeUser("user-d@jorts.io", "user_d", "Ewes Erdi")

		eventTime := time.Date(2020, 1, 30, 3, 45, 33, 0, time.UTC)

		ts.MockGerrit.EXPECT().
			GetChangeEx(gomock.Any(), "jorts/testiness~1", &gerrit.QueryChangesOpts{
				DescribeDetailedAccounts: true,
				DescribeReviewerUpdates:  true,
			}).
			Times(1).
			Return(gerrit.ChangeInfo{
				ID:       "jorts%2Ftestiness~master~Ide512e00237f102c771b7056d3e557586f82272a",
				Project:  "jorts/testiness",
				Branch:   "master",
				ChangeID: "Ide512e00237f102c771b7056d3e557586f82272a",
				Subject:  "beans",
				Status:   "NEW",
				Created:  "2020-01-30 03:43:59.000000000",
				Updated:  "2020-01-30 03:43:59.000000000",
				Number:   1,
				Owner:    *userA.GerritAccount(),
				ReviewerUpdates: []gerrit.ReviewerUpdateInfo{
					{
						Updated:   eventTime.Add(-60 * time.Second).Format(gerrit.TimeLayout),
						UpdatedBy: *userC.GerritAccount(),
						Reviewer:  userB.GerritAccount(),
						State:     "REVIEWER",
					},
					{
						Updated:   eventTime.Add(-30 * time.Second).Format(gerrit.TimeLayout),
						UpdatedBy: *userC.GerritAccount(),
						Reviewer:  userB.GerritAccount(),
						State:     "REMOVED",
					},
					{
						Updated:   eventTime.Format(gerrit.TimeLayout),
						UpdatedBy: *userD.GerritAccount(),
						Reviewer:  userB.GerritAccount(),
						State:     "REVIEWER",
					},
				},
			}, nil)

		// notify new reviewer, including who did the adding
		ts.MockChat.EXPECT().
			SendNotification(gomock.Any(), userB.chatID, stringTrimEq("<@CHATID(user_d)> added you as a reviewer on [testiness@1] <https://gerrit.jorts.io/c/jorts/testiness/+/1|beans>")).
			Times(1).
			Return(nil, nil)

		// notify change owner, including who did the adding
		ts.MockChat.EXPECT().
			SendNotification(gomock.Any(), userA.chatID, stringTrimEq("<@CHATID(user_d)> added <@CHATID(user_b)> as a reviewer on your change [testiness@1] <https://gerrit.jorts.io/c/jorts/testiness/+/1|beans>")).
			Times(1).
			Return(nil, nil)

		// notify notification channel
		ts.MockChat.EXPECT().
			SendChannelNotification(gomock.Any(), "GLOBALNOTIFY", "Ewes Erdi added user_b as a reviewer on change [testiness@1] <https://gerrit.jorts.io/c/jorts/testiness/+/1|beans>").
			Times(1).
			Return(nil, nil)

		ts.InjectEvent(`{
			"change": {
				"project": "jorts/testiness",
				"branch": "master",
				"id": "Ide512e00237f102c771b7056d3e557586f82272a",
				"number": 1,
				"subject": "beans",
				"owner": ` + userA.JSON() + `,
				"url": "https://gerrit.jorts.io/c/jorts/testiness/+/1",
				"commitMessage": "beans\n\nChange-Id: Ide512e00237f102c771b7056d3e557586f82272a\n",
				"status": "NEW"
			},
			"patchSet": {
				"number": 2,
				"revision": "beebeebeebeebeebeebeebeebeebeebeebeebeeb",
				"uploader": ` + userC.JSON() + `,
				"author": ` + userD.JSON() + `,
				"kind": "REWORK"
			},
			"reviewer": ` + userB.JSON() + `,
			"type": "reviewer-added",
			"eventCreatedOn": ` + strconv.FormatInt(eventTime.Add(time.Second).Unix(), 10) + `
		}`)
	})
}

func TestTeamReports(t *testing.T) {
	testWithMockChat(t, map[string]string{
		"gerrit-address":                       "https://gerrit.jorts.io",
		"remove-project-prefix":                "jorts/",
		"global-notify-channel":                "GLOBALNOTIFY",
		"reports.testymctestface.timeofday":    time.Now().Add(-time.Second).UTC().Format("15:04:05.000"),
		"reports.testymctestface.channel":      "channel1",
		"reports.tootymctootface.channel":      "notthischannel",
		"reports.testymctestface.weekends":     "true",
		"reports.testymctestface.gerrit-query": "abc 123 +fourfive -foo",
	}, func(ts *testSystem) {
		// reconfigure the report send time to half a second from now, so we don't have
		// to wait too long in the test
		triggerTime := time.Now().Add(500 * time.Millisecond).UTC()
		ts.App.IncomingChatCommand(adminUserID, "dunno", true, "!config reports.testymctestface.timeofday "+triggerTime.Format("15:04:05.000"))

		ctx, cancel := context.WithTimeout(ts.Ctx, 5*time.Second)
		defer cancel()

		ts.MockGerrit.EXPECT().
			QueryChangesEx(gomock.Any(), gomock.Eq([]string{"abc 123 +fourfive -foo"}), gomock.AssignableToTypeOf(&gerrit.QueryChangesOpts{})).
			Times(1).
			Return([]gerrit.ChangeInfo{}, false, nil)

		ts.MockChat.EXPECT().
			SendChannelReport(gomock.Any(), gomock.Eq("channel1"), gomock.Eq("0 changesets waiting for review (0 waiting for over a week, 0 waiting for over a month)"), gomock.Len(0)).
			Times(1).
			DoAndReturn(func(_ context.Context, channelID, caption string, lines []string) (messages.MessageHandle, error) {
				// don't need to continue the test
				cancel()
				return nil, nil
			})

		err := ts.App.PeriodicTeamReports(ctx, time.Now)
		assert.Equal(t, context.Canceled, err)
	})
}

type stringTrimMatcher struct {
	lines []string
}

func stringTrimEq(s string) *stringTrimMatcher {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return &stringTrimMatcher{lines}
}

func (stm *stringTrimMatcher) Matches(x interface{}) bool {
	s, ok := x.(string)
	if !ok {
		return false
	}
	gotLines := strings.Split(strings.TrimSpace(s), "\n")
	if len(gotLines) != len(stm.lines) {
		return false
	}
	for i, gotLine := range gotLines {
		if strings.TrimSpace(gotLine) != stm.lines[i] {
			return false
		}
	}
	return true
}

func (stm *stringTrimMatcher) String() string {
	return fmt.Sprintf("%q", stm.lines)
}

// Test that a non-max vote by a robot does not indicate that no more reviewers are needed
func TestTeamReportsFiltering(t *testing.T) {
	testData, err := os.Open("sample_change.json")
	require.NoError(t, err)
	sampleChangeInfoJSON, err := io.ReadAll(testData)
	require.NoError(t, err)
	var sampleChangeInfo []gerrit.ChangeInfo
	err = json.Unmarshal(sampleChangeInfoJSON, &sampleChangeInfo)
	require.NoError(t, err)

	testWithMockChat(t, map[string]string{
		"gerrit-address":                       "https://gerrit.jorts.io",
		"remove-project-prefix":                "storj/",
		"global-notify-channel":                "GLOBALNOTIFY",
		"reports.testymctestface.timeofday":    time.Now().Add(-time.Second).UTC().Format("15:04:05.000"),
		"reports.testymctestface.channel":      "channel2",
		"reports.testymctestface.weekends":     "true",
		"reports.testymctestface.gerrit-query": "abc 123 +fourfive -foo",
	}, func(ts *testSystem) {
		// reconfigure the report send time to half a second from now, so we don't have
		// to wait too long in the test
		triggerTime := time.Now().Add(500 * time.Millisecond).UTC()
		ts.App.IncomingChatCommand(adminUserID, "dunno", true, "!config reports.testymctestface.timeofday "+triggerTime.Format("15:04:05.000"))

		ctx, cancel := context.WithTimeout(ts.Ctx, 5*time.Second)
		defer cancel()

		ts.MockGerrit.EXPECT().
			QueryChangesEx(gomock.Any(), gomock.Eq([]string{"abc 123 +fourfive -foo"}), gomock.AssignableToTypeOf(&gerrit.QueryChangesOpts{})).
			Times(1).
			Return(sampleChangeInfo, false, nil)

		ts.MockGerrit.EXPECT().
			URLForChange(gomock.AssignableToTypeOf(&gerrit.ChangeInfo{})).
			Times(1).
			DoAndReturn(func(change *gerrit.ChangeInfo) string {
				return "https://some-gerrit-instance/" + change.ID
			})

		ts.MockChat.EXPECT().
			SendChannelReport(gomock.Any(), gomock.Eq("channel2"), gomock.Eq("1 changesets waiting for review (0 waiting for over a week, 1 waiting for over a month)"), gomock.Len(1)).
			Times(1).
			DoAndReturn(func(_ context.Context, channelID, caption string, lines []string) (messages.MessageHandle, error) {
				// don't need to continue the test
				cancel()
				return nil, nil
			})

		err := ts.App.PeriodicTeamReports(ctx, time.Now)
		assert.Equal(t, context.Canceled, err)
	})
}
