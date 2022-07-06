package kopia

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/internal/model"
	ctesting "github.com/alcionai/corso/internal/testing"
)

type fooModel struct {
	model.BaseModel
	Bar string
}

func getModelStore(t *testing.T, ctx context.Context) *ModelStore {
	c, err := openKopiaRepo(t, ctx)
	require.NoError(t, err)

	return &ModelStore{c}
}

// ---------------
// unit tests
// ---------------
type ModelStoreUnitSuite struct {
	suite.Suite
}

func TestModelStoreUnitSuite(t *testing.T) {
	suite.Run(t, new(ModelStoreUnitSuite))
}
func (suite *ModelStoreUnitSuite) TestCloseWithoutInitDoesNotPanic() {
	assert.NotPanics(suite.T(), func() {
		m := &ModelStore{}
		m.Close(context.Background())
	})
}

// ---------------
// integration tests that use kopia
// ---------------
type ModelStoreIntegrationSuite struct {
	suite.Suite
	ctx context.Context
	m   *ModelStore
}

func TestModelStoreIntegrationSuite(t *testing.T) {
	if err := ctesting.RunOnAny(
		ctesting.CorsoCITests,
		ctesting.CorsoModelStoreTests,
	); err != nil {
		t.Skip()
	}

	suite.Run(t, new(ModelStoreIntegrationSuite))
}

func (suite *ModelStoreIntegrationSuite) SetupSuite() {
	_, err := ctesting.GetRequiredEnvVars(ctesting.AWSStorageCredEnvs...)
	require.NoError(suite.T(), err)
}

func (suite *ModelStoreIntegrationSuite) SetupTest() {
	suite.ctx = context.Background()
	suite.m = getModelStore(suite.T(), suite.ctx)
}

func (suite *ModelStoreIntegrationSuite) TearDownTest() {
	assert.NoError(suite.T(), suite.m.Close(suite.ctx))
}

func (suite *ModelStoreIntegrationSuite) TestBadTagsErrors() {
	table := []struct {
		name string
		tags map[string]string
	}{
		{
			name: "StableIDTag",
			tags: map[string]string{
				stableIDKey: "foo",
			},
		},
		{
			name: "manifestTypeTag",
			tags: map[string]string{
				manifest.TypeLabelKey: "foo",
			},
		},
	}

	for _, test := range table {
		suite.T().Run(test.name, func(t *testing.T) {
			foo := &fooModel{Bar: uuid.NewString()}
			foo.Tags = test.tags

			assert.Error(t, suite.m.Put(suite.ctx, BackupOpModel, foo))
			assert.Error(t, suite.m.Update(suite.ctx, BackupOpModel, foo))

			_, err := suite.m.GetIDsForType(suite.ctx, BackupOpModel, test.tags)
			assert.Error(t, err)
		})
	}
}

func (suite *ModelStoreIntegrationSuite) TestNoIDsErrors() {
	t := suite.T()
	theModelType := BackupOpModel

	noStableID := &fooModel{Bar: uuid.NewString()}
	noStableID.StableID = ""
	noStableID.ModelStoreID = manifest.ID(uuid.NewString())

	noModelStoreID := &fooModel{Bar: uuid.NewString()}
	noModelStoreID.StableID = model.ID(uuid.NewString())
	noModelStoreID.ModelStoreID = ""

	assert.Error(t, suite.m.Update(suite.ctx, theModelType, noStableID))
	assert.Error(t, suite.m.Update(suite.ctx, theModelType, noModelStoreID))

	assert.Error(t, suite.m.Get(suite.ctx, theModelType, "", nil))
	assert.Error(t, suite.m.GetWithModelStoreID(suite.ctx, theModelType, "", nil))

	assert.Error(t, suite.m.Delete(suite.ctx, theModelType, ""))
	assert.Error(t, suite.m.DeleteWithModelStoreID(suite.ctx, ""))
}

func (suite *ModelStoreIntegrationSuite) TestBadModelTypeErrors() {
	t := suite.T()

	foo := &fooModel{Bar: uuid.NewString()}

	assert.Error(t, suite.m.Put(suite.ctx, UnknownModel, foo))

	require.NoError(t, suite.m.Put(suite.ctx, BackupOpModel, foo))

	_, err := suite.m.GetIDsForType(suite.ctx, UnknownModel, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model type")
}

func (suite *ModelStoreIntegrationSuite) TestBadTypeErrors() {
	t := suite.T()

	foo := &fooModel{Bar: uuid.NewString()}

	require.NoError(t, suite.m.Put(suite.ctx, BackupOpModel, foo))

	returned := &fooModel{}
	assert.Error(t, suite.m.Get(suite.ctx, RestoreOpModel, foo.StableID, returned))
	assert.Error(
		t, suite.m.GetWithModelStoreID(suite.ctx, RestoreOpModel, foo.ModelStoreID, returned))

	assert.Error(t, suite.m.Delete(suite.ctx, RestoreOpModel, foo.StableID))
}

func (suite *ModelStoreIntegrationSuite) TestPutGet() {
	table := []struct {
		t      modelType
		check  require.ErrorAssertionFunc
		hasErr bool
	}{
		{
			t:      UnknownModel,
			check:  require.Error,
			hasErr: true,
		},
		{
			t:      BackupOpModel,
			check:  require.NoError,
			hasErr: false,
		},
		{
			t:      RestoreOpModel,
			check:  require.NoError,
			hasErr: false,
		},
		{
			t:      RestorePointModel,
			check:  require.NoError,
			hasErr: false,
		},
	}

	for _, test := range table {
		suite.T().Run(test.t.String(), func(t *testing.T) {
			foo := &fooModel{Bar: uuid.NewString()}
			// Avoid some silly test errors from comparing nil to empty map.
			foo.Tags = map[string]string{}

			err := suite.m.Put(suite.ctx, test.t, foo)
			test.check(t, err)

			if test.hasErr {
				return
			}

			require.NotEmpty(t, foo.ModelStoreID)
			require.NotEmpty(t, foo.StableID)

			returned := &fooModel{}
			err = suite.m.Get(suite.ctx, test.t, foo.StableID, returned)
			require.NoError(t, err)
			assert.Equal(t, foo, returned)

			err = suite.m.GetWithModelStoreID(suite.ctx, test.t, foo.ModelStoreID, returned)
			require.NoError(t, err)
			assert.Equal(t, foo, returned)
		})
	}
}

func (suite *ModelStoreIntegrationSuite) TestPutGet_WithTags() {
	t := suite.T()
	theModelType := BackupOpModel

	foo := &fooModel{Bar: uuid.NewString()}
	foo.Tags = map[string]string{
		"bar": "baz",
	}

	require.NoError(t, suite.m.Put(suite.ctx, theModelType, foo))

	require.NotEmpty(t, foo.ModelStoreID)
	require.NotEmpty(t, foo.StableID)

	returned := &fooModel{}
	err := suite.m.Get(suite.ctx, theModelType, foo.StableID, returned)
	require.NoError(t, err)
	assert.Equal(t, foo, returned)

	err = suite.m.GetWithModelStoreID(suite.ctx, theModelType, foo.ModelStoreID, returned)
	require.NoError(t, err)
	assert.Equal(t, foo, returned)
}

func (suite *ModelStoreIntegrationSuite) TestGet_NotFoundErrors() {
	t := suite.T()

	assert.ErrorIs(t, suite.m.Get(suite.ctx, BackupOpModel, "baz", nil), manifest.ErrNotFound)
	assert.ErrorIs(
		t, suite.m.GetWithModelStoreID(suite.ctx, BackupOpModel, "baz", nil), manifest.ErrNotFound)
}

func (suite *ModelStoreIntegrationSuite) TestPutGetOfType() {
	table := []struct {
		t      modelType
		check  require.ErrorAssertionFunc
		hasErr bool
	}{
		{
			t:      UnknownModel,
			check:  require.Error,
			hasErr: true,
		},
		{
			t:      BackupOpModel,
			check:  require.NoError,
			hasErr: false,
		},
		{
			t:      RestoreOpModel,
			check:  require.NoError,
			hasErr: false,
		},
		{
			t:      RestorePointModel,
			check:  require.NoError,
			hasErr: false,
		},
	}

	for _, test := range table {
		suite.T().Run(test.t.String(), func(t *testing.T) {
			foo := &fooModel{Bar: uuid.NewString()}

			err := suite.m.Put(suite.ctx, test.t, foo)
			test.check(t, err)

			if test.hasErr {
				return
			}

			ids, err := suite.m.GetIDsForType(suite.ctx, test.t, nil)
			require.NoError(t, err)

			assert.Len(t, ids, 1)
		})
	}
}

func (suite *ModelStoreIntegrationSuite) TestPutUpdate() {
	table := []struct {
		name    string
		mutator func(m *fooModel)
	}{
		{
			name: "NoTags",
			mutator: func(m *fooModel) {
				m.Bar = "baz"
			},
		},
		{
			name: "WithTags",
			mutator: func(m *fooModel) {
				m.Bar = "baz"
				m.Tags = map[string]string{
					"a": "42",
				}
			},
		},
	}

	for _, test := range table {
		suite.T().Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			theModelType := BackupOpModel

			m := getModelStore(t, ctx)
			defer func() {
				assert.NoError(t, m.c.Close(ctx))
			}()

			foo := &fooModel{Bar: uuid.NewString()}
			// Avoid some silly test errors from comparing nil to empty map.
			foo.Tags = map[string]string{}

			require.NoError(t, m.Put(ctx, theModelType, foo))

			oldModelID := foo.ModelStoreID
			oldStableID := foo.StableID

			test.mutator(foo)

			require.NoError(t, m.Update(ctx, theModelType, foo))
			assert.Equal(t, oldStableID, foo.StableID)

			returned := &fooModel{}
			require.NoError(
				t, m.GetWithModelStoreID(ctx, theModelType, foo.ModelStoreID, returned))
			assert.Equal(t, foo, returned)

			ids, err := m.GetIDsForType(ctx, theModelType, nil)
			require.NoError(t, err)
			assert.Len(t, ids, 1)

			if oldModelID == foo.ModelStoreID {
				// Unlikely, but we don't control ModelStoreID generation and can't
				// guarantee this won't happen.
				return
			}

			err = m.GetWithModelStoreID(ctx, theModelType, oldModelID, nil)
			assert.ErrorIs(t, err, manifest.ErrNotFound)
		})
	}
}

func (suite *ModelStoreIntegrationSuite) TestPutUpdate_FailsNotMatchingPrev() {
	startModelType := BackupOpModel

	table := []struct {
		name    string
		t       modelType
		mutator func(m *fooModel)
	}{
		{
			name: "DifferentModelStoreID",
			t:    startModelType,
			mutator: func(m *fooModel) {
				m.ModelStoreID = manifest.ID("bar")
			},
		},
		{
			name: "DifferentModelType",
			t:    RestoreOpModel,
			mutator: func(m *fooModel) {
			},
		},
	}

	for _, test := range table {
		suite.T().Run(test.name, func(t *testing.T) {
			ctx := context.Background()

			m := getModelStore(t, ctx)
			defer func() {
				assert.NoError(t, m.c.Close(ctx))
			}()

			foo := &fooModel{Bar: uuid.NewString()}

			require.NoError(t, m.Put(ctx, startModelType, foo))

			test.mutator(foo)

			assert.Error(t, m.Update(ctx, test.t, foo))
		})
	}
}

func (suite *ModelStoreIntegrationSuite) TestPutDelete() {
	t := suite.T()
	theModelType := BackupOpModel

	foo := &fooModel{Bar: uuid.NewString()}

	require.NoError(t, suite.m.Put(suite.ctx, theModelType, foo))

	require.NoError(t, suite.m.Delete(suite.ctx, theModelType, foo.StableID))

	returned := &fooModel{}
	err := suite.m.GetWithModelStoreID(suite.ctx, theModelType, foo.ModelStoreID, returned)
	assert.ErrorIs(t, err, manifest.ErrNotFound)
}

func (suite *ModelStoreIntegrationSuite) TestPutDelete_BadIDsNoop() {
	t := suite.T()

	assert.NoError(t, suite.m.Delete(suite.ctx, BackupOpModel, "foo"))
	assert.NoError(t, suite.m.DeleteWithModelStoreID(suite.ctx, "foo"))
}

// ---------------
// regression tests that use kopia
// ---------------
type ModelStoreRegressionSuite struct {
	suite.Suite
}

func TestModelStoreRegressionSuite(t *testing.T) {
	if err := ctesting.RunOnAny(
		ctesting.CorsoCITests,
		ctesting.CorsoModelStoreTests,
	); err != nil {
		t.Skip()
	}

	suite.Run(t, new(ModelStoreRegressionSuite))
}

func (suite *ModelStoreRegressionSuite) SetupSuite() {
	_, err := ctesting.GetRequiredEnvVars(ctesting.AWSStorageCredEnvs...)
	require.NoError(suite.T(), err)
}

// TODO(ashmrtn): Make a mock of whatever controls the handle to kopia so we can
// ask it to fail on arbitrary function, thus allowing us to directly test
// Update.
// Tests that if we get an error or crash while in the middle of an Update no
// results will be visible to higher layers.
func (suite *ModelStoreRegressionSuite) TestFailDuringWriteSessionHasNoVisibleEffect() {
	ctx := context.Background()
	t := suite.T()

	m := getModelStore(t, ctx)
	defer func() {
		assert.NoError(t, m.c.Close(ctx))
	}()

	foo := &fooModel{Bar: uuid.NewString()}
	foo.StableID = model.ID(uuid.NewString())
	foo.ModelStoreID = manifest.ID(uuid.NewString())
	// Avoid some silly test errors from comparing nil to empty map.
	foo.Tags = map[string]string{}

	theModelType := BackupOpModel

	require.NoError(t, m.Put(ctx, theModelType, foo))

	newID := manifest.ID("")
	err := repo.WriteSession(
		ctx,
		m.c,
		repo.WriteSessionOptions{Purpose: "WriteSessionFailureTest"},
		func(innerCtx context.Context, w repo.RepositoryWriter) (innerErr error) {
			base := foo.Base()
			oldID := base.ModelStoreID

			defer func() {
				if innerErr != nil {
					// Restore the old ID if we failed.
					base.ModelStoreID = oldID
				}
			}()

			innerErr = putInner(innerCtx, w, theModelType, foo, false)
			require.NoError(t, innerErr)

			newID = foo.ModelStoreID

			return assert.AnError
		},
	)

	assert.ErrorIs(t, err, assert.AnError)

	err = m.GetWithModelStoreID(ctx, theModelType, newID, nil)
	assert.ErrorIs(t, err, manifest.ErrNotFound)

	returned := &fooModel{}
	require.NoError(
		t, m.GetWithModelStoreID(ctx, theModelType, foo.ModelStoreID, returned))
	assert.Equal(t, foo, returned)
}
