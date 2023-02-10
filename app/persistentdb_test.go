package app

import (
	"context"
	"database/sql"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func doPersistentDBTest(t *testing.T, f func(ctx context.Context, d *PersistentDB)) {
	dbFile := path.Join(t.TempDir(), "persistent.db")
	db, err := NewPersistentDB(zaptest.NewLogger(t), "sqlite:"+dbFile)
	require.NoError(t, err)
	require.NotNil(t, db)
	defer func() { require.NoError(t, db.Close()) }()

	f(context.Background(), db)
}

func TestPersistentDBBasics(t *testing.T) {
	const gerritUsername = "noodle"
	const chatID = "U1E9A928BCD"

	doPersistentDBTest(t, func(ctx context.Context, db *PersistentDB) {
		// looking up a nonexistent name should fail
		got, err := db.LookupChatIDForGerritUser(ctx, gerritUsername)
		require.Equal(t, sql.ErrNoRows, err)
		require.Equal(t, "", got)

		// inserting a new name should work
		err = db.AssociateChatIDWithGerritUser(ctx, gerritUsername, chatID)
		require.NoError(t, err)

		// trying to insert again should fail
		err = db.AssociateChatIDWithGerritUser(ctx, gerritUsername, chatID)
		require.Error(t, err)

		// inserted name can be retrieved
		got, err = db.LookupChatIDForGerritUser(ctx, gerritUsername)
		require.NoError(t, err)
		require.Equal(t, chatID, got)
	})
}
