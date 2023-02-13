package app

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/zeebo/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/storj/changesetchihuahua/app/dbx"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	prunePeriod       = flag.Duration("db-prune-period", time.Hour, "Time between persistent db prune jobs")
	pruneTimeout      = flag.Duration("db-prune-timeout", 10*time.Minute, "Cancel any prune jobs that run longer than this amount of time")
	buildLifetimeDays = flag.Int("build-lifetime-days", 7, "Builds on patchsets older than this many days will not have their announcements inline-annotated with new build statuses")
)

type PersistentDB struct {
	logger *zap.Logger
	db     *dbx.DB
	dbLock sync.Mutex // is this still necessary with sqlite?

	cacheLock sync.RWMutex
	cache     map[string]string

	pruneCancel context.CancelFunc
}

func NewPersistentDB(logger *zap.Logger, dbSource string) (*PersistentDB, error) {
	db, err := initializePersistentDB(logger, dbSource)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	pdb := &PersistentDB{
		logger:      logger,
		db:          db,
		cache:       make(map[string]string),
		pruneCancel: cancel,
	}
	go pdb.pruneJob(ctx)
	return pdb, nil
}

func openPersistentDB(dbSource string) (*dbx.DB, string, error) {
	sourceSplit := strings.SplitN(dbSource, ":", 2)
	if len(sourceSplit) == 1 {
		return nil, "", errs.New("Invalid data source: %q. Example: sqlite:foo.db", dbSource)
	}
	driverName := sourceSplit[0]
	switch driverName {
	case "sqlite", "sqlite3":
		driverName = "sqlite3"
		dbSource = sourceSplit[1]
	case "postgres", "postgresql":
		driverName = "postgres"
	default:
		return nil, "", errs.New("unrecognized database driver name %q", driverName)
	}

	dbxDB, err := dbx.Open(driverName, dbSource)
	return dbxDB, driverName, err
}

func initializePersistentDB(logger *zap.Logger, dbSource string) (*dbx.DB, error) {
	logger.Info("Opening persistent DB", zap.String("db-source", dbSource))
	db, driverName, err := openPersistentDB(dbSource)
	if err != nil {
		return nil, err
	}

	migrationSource, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		log.Fatal(err)
	}

	var migrationTarget database.Driver
	switch driverName {
	case "sqlite3":
		migrationTarget, err = sqlite3.WithInstance(db.DB, &sqlite3.Config{})
	case "postgres":
		migrationTarget, err = postgres.WithInstance(db.DB, &postgres.Config{})
	}
	if err != nil {
		return nil, err
	}

	migrator, err := migrate.NewWithInstance("iofs", migrationSource, "persistent-db", migrationTarget)
	if err != nil {
		return nil, err
	}
	migrator.Log = newMigrateLogWrapper(logger)

	if err := migrator.Up(); err != nil {
		if err != migrate.ErrNoChange {
			return nil, err
		}
	}

	return db, nil
}

func (ud *PersistentDB) Close() error {
	ud.pruneCancel()
	return ud.db.Close()
}

func (ud *PersistentDB) pruneJob(ctx context.Context) {
	ticker := time.NewTicker(*prunePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			go func() {
				jobCtx, cancel := context.WithTimeout(ctx, *pruneTimeout)
				defer cancel()
				if err := ud.Prune(jobCtx, t); err != nil {
					ud.logger.Error("Prune job failed", zap.Error(err))
				}
			}()
		}
	}
}

func (ud *PersistentDB) LookupGerritUser(ctx context.Context, gerritUsername string) (*dbx.GerritUser, error) {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	return ud.db.Get_GerritUser_By_GerritUsername(ctx, dbx.GerritUser_GerritUsername(gerritUsername))
}

func (ud *PersistentDB) LookupChatIDForGerritUser(ctx context.Context, gerritUsername string) (string, error) {
	// check cache
	ud.cacheLock.RLock()
	chatID, found := ud.cache[gerritUsername]
	ud.cacheLock.RUnlock()
	if found {
		return chatID, nil
	}
	// consult db if necessary
	usermapRecord, err := ud.LookupGerritUser(ctx, gerritUsername)
	if err != nil {
		return "", err
	}
	chatID = usermapRecord.ChatId

	// update cache if successful
	ud.cacheLock.Lock()
	ud.cache[gerritUsername] = chatID
	ud.cacheLock.Unlock()

	return chatID, nil
}

func (ud *PersistentDB) AssociateChatIDWithGerritUser(ctx context.Context, gerritUsername, chatID string) error {
	err := func() error {
		ud.dbLock.Lock()
		defer ud.dbLock.Unlock()

		return ud.db.CreateNoReturn_GerritUser(ctx, dbx.GerritUser_GerritUsername(gerritUsername), dbx.GerritUser_ChatId(chatID), dbx.GerritUser_Create_Fields{})
	}()
	if err != nil {
		return err
	}
	// if update was successful, this call is responsible for adding to cache
	ud.cacheLock.Lock()
	ud.cache[gerritUsername] = chatID
	ud.cacheLock.Unlock()

	ud.logger.Debug("associated gerrit user to chat ID",
		zap.String("gerrit-username", gerritUsername),
		zap.String("chat-id", chatID))
	return nil
}

func (ud *PersistentDB) GetAllUsersWhoseLastReportWasBefore(ctx context.Context, t time.Time) ([]*dbx.GerritUser, error) {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	return ud.db.All_GerritUser_By_LastReport_Less(ctx, dbx.GerritUser_LastReport(t))
}

func (ud *PersistentDB) UpdateLastReportTime(ctx context.Context, gerritUsername string, when time.Time) error {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	return ud.db.UpdateNoReturn_GerritUser_By_GerritUsername(ctx,
		dbx.GerritUser_GerritUsername(gerritUsername),
		dbx.GerritUser_Update_Fields{LastReport: dbx.GerritUser_LastReport(when)})
}

func (ud *PersistentDB) IdentifyNewInlineComments(ctx context.Context, commentsByID map[string]time.Time) (err error) {
	if len(commentsByID) == 0 {
		return nil
	}
	alternatives := make([]string, 0, len(commentsByID))
	queryArgs := make([]interface{}, 0, len(commentsByID))
	for commentID := range commentsByID {
		alternatives = append(alternatives, "comment_id = ?")
		queryArgs = append(queryArgs, commentID)
	}
	query := `SELECT comment_id FROM inline_comments WHERE (` + strings.Join(alternatives, " OR ") + `)`

	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	rows, err := ud.db.DB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return err
	}
	defer func() { err = errs.Combine(err, rows.Close()) }()

	for rows.Next() {
		var foundCommentID string
		if err := rows.Scan(&foundCommentID); err != nil {
			return err
		}
		delete(commentsByID, foundCommentID)
	}

	if len(commentsByID) > 0 {
		values := make([]string, 0, len(commentsByID))
		queryArgs := make([]interface{}, 0, len(commentsByID)*2)
		for commentID, timeStamp := range commentsByID {
			values = append(values, "(?, ?)")
			queryArgs = append(queryArgs, commentID, timeStamp.UTC())
		}
		query := `INSERT INTO inline_comments (comment_id, updated_at) VALUES ` + strings.Join(values, ", ") + ` ON CONFLICT (comment_id) DO UPDATE SET updated_at = EXCLUDED.updated_at`
		_, err := ud.db.ExecContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ud *PersistentDB) GetPatchSetAnnouncements(ctx context.Context, projectName string, changeNum, patchSetNum int) ([]string, error) {
	rows, err := ud.db.All_PatchsetAnnouncement_MessageHandle_By_ProjectName_And_ChangeNum_And_PatchsetNum(
		ctx,
		dbx.PatchsetAnnouncement_ProjectName(projectName),
		dbx.PatchsetAnnouncement_ChangeNum(changeNum),
		dbx.PatchsetAnnouncement_PatchsetNum(patchSetNum))
	if err != nil {
		return nil, err
	}
	handles := make([]string, len(rows))
	for i, row := range rows {
		handles[i] = row.MessageHandle
	}
	return handles, nil
}

func (ud *PersistentDB) RecordPatchSetAnnouncements(ctx context.Context, projectName string, changeNum, patchSetNum int, announcementHandles []string) error {
	var allErrors error
	for _, handle := range announcementHandles {
		err := ud.db.CreateNoReturn_PatchsetAnnouncement(ctx,
			dbx.PatchsetAnnouncement_ProjectName(projectName),
			dbx.PatchsetAnnouncement_ChangeNum(changeNum),
			dbx.PatchsetAnnouncement_PatchsetNum(patchSetNum),
			dbx.PatchsetAnnouncement_MessageHandle(handle))
		allErrors = errs.Combine(allErrors, err)
	}
	return allErrors
}

func (ud *PersistentDB) GetAllConfigItems(ctx context.Context) (map[string]string, error) {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	items, err := ud.db.All_TeamConfig(ctx)
	if err != nil {
		return nil, err
	}
	itemsMap := make(map[string]string)
	for _, item := range items {
		itemsMap[item.ConfigKey] = item.ConfigValue
	}
	return itemsMap, nil
}

func (ud *PersistentDB) GetConfig(ctx context.Context, key, defaultValue string) (string, error) {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	value, err := ud.db.Get_TeamConfig_ConfigValue_By_ConfigKey(ctx, dbx.TeamConfig_ConfigKey(key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return defaultValue, err
	}
	return value.ConfigValue, nil
}

func (ud *PersistentDB) JustGetConfig(ctx context.Context, key, defaultValue string) string {
	val, err := ud.GetConfig(ctx, key, defaultValue)
	if err != nil {
		ud.logger.Error("failed to retrieve value in config db", zap.String("key", key), zap.Error(err))
	}
	return val
}

func (ud *PersistentDB) GetConfigInt(ctx context.Context, key string, defaultValue int) (int, error) {
	val, err := ud.GetConfig(ctx, key, "")
	if err != nil || val == "" {
		return defaultValue, err
	}
	numVal, err := strconv.ParseInt(val, 0, 32)
	if err != nil {
		return defaultValue, err
	}
	return int(numVal), nil
}

func (ud *PersistentDB) JustGetConfigInt(ctx context.Context, key string, defaultValue int) int {
	val, err := ud.GetConfigInt(ctx, key, defaultValue)
	if err != nil {
		ud.logger.Info("failed to retrieve int value in config db", zap.String("key", key), zap.Error(err))
	}
	return val
}

func (ud *PersistentDB) GetConfigBool(ctx context.Context, key string, defaultValue bool) (bool, error) {
	val, err := ud.GetConfig(ctx, key, "")
	if err != nil || val == "" {
		return defaultValue, err
	}
	val = strings.ToLower(val)
	switch val {
	case "yes", "y", "1", "on", "true", "t":
		return true, nil
	case "no", "n", "0", "off", "false", "f":
		return false, nil
	}
	return defaultValue, fmt.Errorf("invalid boolean value %q", val)
}

func (ud *PersistentDB) JustGetConfigBool(ctx context.Context, key string, defaultValue bool) bool {
	val, err := ud.GetConfigBool(ctx, key, defaultValue)
	if err != nil {
		ud.logger.Info("failed to retrieve bool value in config db", zap.String("key", key), zap.Error(err))
	}
	return val
}

func (ud *PersistentDB) GetConfigWildcard(ctx context.Context, pattern string) (items map[string]string, err error) {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	rows, err := ud.db.DB.QueryContext(ctx, ud.db.Rebind(`
		SELECT config_key, config_value FROM team_configs
		WHERE config_key LIKE ?
	`), pattern)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeErr := rows.Close()
		queryErr := rows.Err()
		if err == nil {
			if closeErr != nil {
				err = closeErr
			}
			if queryErr != nil {
				err = queryErr
			}
		}
	}()

	items = make(map[string]string)
	for rows.Next() {
		var key, val string
		err = rows.Scan(&key, &val)
		if err != nil {
			return nil, err
		}
		items[key] = val
	}
	return items, nil
}

func (ud *PersistentDB) JustGetConfigWildcard(ctx context.Context, pattern string) map[string]string {

	items, err := ud.GetConfigWildcard(ctx, pattern)
	if err != nil {
		ud.logger.Error("failed to retrieve config keys by pattern", zap.String("pattern", pattern), zap.Error(err))
		return nil
	}
	return items
}

func (ud *PersistentDB) SetConfig(ctx context.Context, key, value string) error {
	ud.dbLock.Lock()
	defer ud.dbLock.Unlock()

	_, err := ud.db.DB.ExecContext(ctx, ud.db.Rebind(`
		INSERT INTO team_configs (config_key, config_value) VALUES (?, ?)
		ON CONFLICT (config_key) DO UPDATE SET config_value = EXCLUDED.config_value
	`), key, value)
	return err
}

func (ud *PersistentDB) SetConfigInt(ctx context.Context, key string, value int) error {
	return ud.SetConfig(ctx, key, strconv.FormatInt(int64(value), 32))
}

func (ud *PersistentDB) Prune(ctx context.Context, now time.Time) error {
	deleteInlineCommentsBefore := now.Add(-2 * *inlineCommentMaxAge)
	_, err := ud.db.Delete_InlineComment_By_UpdatedAt_Less(ctx, dbx.InlineComment_UpdatedAt(deleteInlineCommentsBefore))
	if err != nil {
		return err
	}
	deletePatchsetAnnouncementsBefore := now.AddDate(0, 0, -*buildLifetimeDays)
	_, err = ud.db.Delete_PatchsetAnnouncement_By_Ts_Less(ctx, dbx.PatchsetAnnouncement_Ts(deletePatchsetAnnouncementsBefore))
	return err
}

// newMigrateLogWrapper is used to wrap a zap.Logger in a way that is usable
// by golang-migrate.
func newMigrateLogWrapper(logger *zap.Logger) migrateLogWrapper {
	verboseWanted := logger.Check(zapcore.DebugLevel, "") != nil
	sugar := logger.Named("migrate").WithOptions(zap.AddCallerSkip(1)).Sugar()
	return migrateLogWrapper{
		logger:  sugar,
		verbose: verboseWanted,
	}
}

type migrateLogWrapper struct {
	logger  *zap.SugaredLogger
	verbose bool
}

func (w migrateLogWrapper) Printf(format string, v ...interface{}) {
	format = strings.TrimRight(format, "\n")
	w.logger.Infof(format, v...)
}

func (w migrateLogWrapper) Verbose() bool {
	return w.verbose
}
