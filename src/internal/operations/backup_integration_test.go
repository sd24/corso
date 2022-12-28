package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/connector/exchange"
	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/events"
	evmock "github.com/alcionai/corso/src/internal/events/mock"
	"github.com/alcionai/corso/src/internal/kopia"
	"github.com/alcionai/corso/src/internal/model"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/pkg/account"
	"github.com/alcionai/corso/src/pkg/backup"
	"github.com/alcionai/corso/src/pkg/control"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/selectors"
	"github.com/alcionai/corso/src/pkg/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

//revive:disable:context-as-argument
func prepNewBackupOp(
	t *testing.T,
	ctx context.Context,
	bus events.Eventer,
	sel selectors.Selector,
) (BackupOperation, account.Account, *kopia.Wrapper, *kopia.ModelStore, func()) {
	//revive:enable:context-as-argument
	acct := tester.NewM365Account(t)

	// need to initialize the repository before we can test connecting to it.
	st := tester.NewPrefixedS3Storage(t)

	k := kopia.NewConn(st)
	require.NoError(t, k.Initialize(ctx))

	// kopiaRef comes with a count of 1 and Wrapper bumps it again so safe
	// to close here.
	closer := func() { k.Close(ctx) }

	kw, err := kopia.NewWrapper(k)
	if !assert.NoError(t, err) {
		closer()
		t.FailNow()
	}

	closer = func() {
		k.Close(ctx)
		kw.Close(ctx)
	}

	ms, err := kopia.NewModelStore(k)
	if !assert.NoError(t, err) {
		closer()
		t.FailNow()
	}

	closer = func() {
		k.Close(ctx)
		kw.Close(ctx)
		ms.Close(ctx)
	}

	sw := store.NewKopiaStore(ms)

	bo, err := NewBackupOperation(
		ctx,
		control.Options{},
		kw,
		sw,
		acct,
		sel,
		bus)
	if !assert.NoError(t, err) {
		closer()
		t.FailNow()
	}

	return bo, acct, kw, ms, closer
}

//revive:disable:context-as-argument
func checkMetadataFilesExist(
	t *testing.T,
	ctx context.Context,
	backupID model.StableID,
	kw *kopia.Wrapper,
	ms *kopia.ModelStore,
	tenant, user string,
	service path.ServiceType,
	category path.CategoryType,
	files []string,
) {
	//revive:enable:context-as-argument
	bup := &backup.Backup{}

	err := ms.Get(ctx, model.BackupSchema, backupID, bup)
	if !assert.NoError(t, err) {
		return
	}

	paths := []path.Path{}
	pathsByRef := map[string][]string{}

	for _, fName := range files {
		p, err := path.Builder{}.
			Append(fName).
			ToServiceCategoryMetadataPath(tenant, user, service, category, true)
		if !assert.NoError(t, err, "bad metadata path") {
			continue
		}

		dir, err := p.Dir()
		if !assert.NoError(t, err, "parent path") {
			continue
		}

		paths = append(paths, p)
		pathsByRef[dir.ShortRef()] = append(pathsByRef[dir.ShortRef()], fName)
	}

	cols, err := kw.RestoreMultipleItems(ctx, bup.SnapshotID, paths, nil)
	assert.NoError(t, err)

	for _, col := range cols {
		itemNames := []string{}

		for item := range col.Items() {
			assert.Implements(t, (*data.StreamSize)(nil), item)

			s := item.(data.StreamSize)
			assert.Greaterf(
				t,
				s.Size(),
				int64(0),
				"empty metadata file: %s/%s",
				col.FullPath(),
				item.UUID(),
			)

			itemNames = append(itemNames, item.UUID())
		}

		assert.ElementsMatchf(
			t,
			pathsByRef[col.FullPath().ShortRef()],
			itemNames,
			"collection %s missing expected files",
			col.FullPath(),
		)
	}
}

// ---------------------------------------------------------------------------
// integration tests
// ---------------------------------------------------------------------------

type BackupOpIntegrationSuite struct {
	suite.Suite
}

func TestBackupOpIntegrationSuite(t *testing.T) {
	if err := tester.RunOnAny(
		tester.CorsoCITests,
		tester.CorsoOperationTests,
		tester.CorsoOperationBackupTests,
	); err != nil {
		t.Skip(err)
	}

	suite.Run(t, new(BackupOpIntegrationSuite))
}

func (suite *BackupOpIntegrationSuite) SetupSuite() {
	_, err := tester.GetRequiredEnvSls(
		tester.AWSStorageCredEnvs,
		tester.M365AcctCredEnvs)
	require.NoError(suite.T(), err)
}

func (suite *BackupOpIntegrationSuite) TestNewBackupOperation() {
	kw := &kopia.Wrapper{}
	sw := &store.Wrapper{}
	acct := tester.NewM365Account(suite.T())

	table := []struct {
		name     string
		opts     control.Options
		kw       *kopia.Wrapper
		sw       *store.Wrapper
		acct     account.Account
		targets  []string
		errCheck assert.ErrorAssertionFunc
	}{
		{"good", control.Options{}, kw, sw, acct, nil, assert.NoError},
		{"missing kopia", control.Options{}, nil, sw, acct, nil, assert.Error},
		{"missing modelstore", control.Options{}, kw, nil, acct, nil, assert.Error},
	}
	for _, test := range table {
		suite.T().Run(test.name, func(t *testing.T) {
			ctx, flush := tester.NewContext()
			defer flush()

			_, err := NewBackupOperation(
				ctx,
				test.opts,
				test.kw,
				test.sw,
				test.acct,
				selectors.Selector{},
				evmock.NewBus())
			test.errCheck(t, err)
		})
	}
}

// TestBackup_Run ensures that Integration Testing works
// for the following scopes: Contacts, Events, and Mail
func (suite *BackupOpIntegrationSuite) TestBackup_Run_exchange() {
	ctx, flush := tester.NewContext()
	defer flush()

	m365UserID := tester.M365UserID(suite.T())
	users := []string{m365UserID}

	tests := []struct {
		name          string
		selectFunc    func() *selectors.ExchangeBackup
		resourceOwner string
		category      path.CategoryType
		metadataFiles []string
	}{
		{
			name: "Mail",
			selectFunc: func() *selectors.ExchangeBackup {
				sel := selectors.NewExchangeBackup(users)
				sel.Include(sel.MailFolders(users, []string{exchange.DefaultMailFolder}, selectors.PrefixMatch()))
				return sel
			},
			resourceOwner: m365UserID,
			category:      path.EmailCategory,
			metadataFiles: exchange.MetadataFileNames(path.EmailCategory),
		},
		{
			name: "Contacts",
			selectFunc: func() *selectors.ExchangeBackup {
				sel := selectors.NewExchangeBackup(users)
				sel.Include(sel.ContactFolders(
					users,
					[]string{exchange.DefaultContactFolder},
					selectors.PrefixMatch()))

				return sel
			},
			resourceOwner: m365UserID,
			category:      path.ContactsCategory,
			metadataFiles: exchange.MetadataFileNames(path.ContactsCategory),
		},
		{
			name: "Calendar Events",
			selectFunc: func() *selectors.ExchangeBackup {
				sel := selectors.NewExchangeBackup(users)
				sel.Include(sel.EventCalendars(users, []string{exchange.DefaultCalendar}, selectors.PrefixMatch()))
				return sel
			},
			resourceOwner: m365UserID,
			category:      path.EventsCategory,
			metadataFiles: exchange.MetadataFileNames(path.EventsCategory),
		},
	}
	for _, test := range tests {
		suite.T().Run(test.name, func(t *testing.T) {
			mb := evmock.NewBus()
			sel := test.selectFunc()
			bo, acct, kw, ms, closer := prepNewBackupOp(t, ctx, mb, sel.Selector)
			defer closer()

			failed := false

			require.NoError(t, bo.Run(ctx))
			require.NotEmpty(t, bo.Results)
			require.NotEmpty(t, bo.Results.BackupID)

			if !assert.Equalf(
				t,
				Completed,
				bo.Status,
				"backup status %s is not Completed",
				bo.Status,
			) {
				failed = true
			}

			if !assert.Less(t, 0, bo.Results.ItemsWritten) {
				failed = true
			}

			assert.Less(t, 0, bo.Results.ItemsRead)
			assert.Less(t, int64(0), bo.Results.BytesRead, "bytes read")
			assert.Less(t, int64(0), bo.Results.BytesUploaded, "bytes uploaded")
			assert.Equal(t, 1, bo.Results.ResourceOwners)
			assert.NoError(t, bo.Results.ReadErrors)
			assert.NoError(t, bo.Results.WriteErrors)
			assert.Equal(t, 1, mb.TimesCalled[events.BackupStart], "backup-start events")
			assert.Equal(t, 1, mb.TimesCalled[events.BackupEnd], "backup-end events")
			assert.Equal(t,
				mb.CalledWith[events.BackupStart][0][events.BackupID],
				bo.Results.BackupID, "backupID pre-declaration")

			// verify that we can find the new backup id in the manifests
			var (
				sck, scv = kopia.MakeServiceCat(sel.PathService(), test.category)
				oc       = &kopia.OwnersCats{
					ResourceOwners: map[string]struct{}{test.resourceOwner: {}},
					ServiceCats:    map[string]kopia.ServiceCat{sck: scv},
				}
				tags  = map[string]string{kopia.TagBackupCategory: ""}
				found bool
			)

			mans, err := kw.FetchPrevSnapshotManifests(ctx, oc, tags)
			assert.NoError(t, err)

			for _, man := range mans {
				tk, _ := kopia.MakeTagKV(kopia.TagBackupID)
				if man.Tags[tk] == string(bo.Results.BackupID) {
					found = true
					break
				}
			}

			assert.True(t, found, "backup retrieved by previous snapshot manifest")

			if failed {
				return
			}

			m365, err := acct.M365Config()
			require.NoError(t, err)

			checkMetadataFilesExist(
				t,
				ctx,
				bo.Results.BackupID,
				kw,
				ms,
				m365.AzureTenantID,
				m365UserID,
				path.ExchangeService,
				test.category,
				test.metadataFiles,
			)
		})
	}
}

func (suite *BackupOpIntegrationSuite) TestBackup_Run_oneDrive() {
	ctx, flush := tester.NewContext()
	defer flush()

	var (
		t          = suite.T()
		mb         = evmock.NewBus()
		m365UserID = tester.SecondaryM365UserID(t)
		sel        = selectors.NewOneDriveBackup([]string{m365UserID})
	)

	sel.Include(sel.Users([]string{m365UserID}))

	bo, _, _, _, closer := prepNewBackupOp(t, ctx, mb, sel.Selector)
	defer closer()

	require.NoError(t, bo.Run(ctx))
	require.NotEmpty(t, bo.Results)
	require.NotEmpty(t, bo.Results.BackupID)
	assert.Equalf(t, Completed, bo.Status, "backup status %s is not Completed", bo.Status)
	assert.Equal(t, bo.Results.ItemsRead, bo.Results.ItemsWritten)
	assert.Less(t, int64(0), bo.Results.BytesRead, "bytes read")
	assert.Less(t, int64(0), bo.Results.BytesUploaded, "bytes uploaded")
	assert.Equal(t, 1, bo.Results.ResourceOwners)
	assert.NoError(t, bo.Results.ReadErrors)
	assert.NoError(t, bo.Results.WriteErrors)
	assert.Equal(t, 1, mb.TimesCalled[events.BackupStart], "backup-start events")
	assert.Equal(t, 1, mb.TimesCalled[events.BackupEnd], "backup-end events")
	assert.Equal(t,
		mb.CalledWith[events.BackupStart][0][events.BackupID],
		bo.Results.BackupID, "backupID pre-declaration")
}

func (suite *BackupOpIntegrationSuite) TestBackup_Run_sharePoint() {
	ctx, flush := tester.NewContext()
	defer flush()

	var (
		t      = suite.T()
		mb     = evmock.NewBus()
		siteID = tester.M365SiteID(t)
		sel    = selectors.NewSharePointBackup([]string{siteID})
	)

	sel.Include(sel.Sites([]string{siteID}))

	bo, _, _, _, closer := prepNewBackupOp(t, ctx, mb, sel.Selector)
	defer closer()

	require.NoError(t, bo.Run(ctx))
	require.NotEmpty(t, bo.Results)
	require.NotEmpty(t, bo.Results.BackupID)
	assert.Equalf(t, Completed, bo.Status, "backup status %s is not Completed", bo.Status)
	assert.Equal(t, bo.Results.ItemsRead, bo.Results.ItemsWritten)
	assert.Less(t, int64(0), bo.Results.BytesRead, "bytes read")
	assert.Less(t, int64(0), bo.Results.BytesUploaded, "bytes uploaded")
	assert.Equal(t, 1, bo.Results.ResourceOwners)
	assert.NoError(t, bo.Results.ReadErrors)
	assert.NoError(t, bo.Results.WriteErrors)
	assert.Equal(t, 1, mb.TimesCalled[events.BackupStart], "backup-start events")
	assert.Equal(t, 1, mb.TimesCalled[events.BackupEnd], "backup-end events")
	assert.Equal(t,
		mb.CalledWith[events.BackupStart][0][events.BackupID],
		bo.Results.BackupID, "backupID pre-declaration")
}