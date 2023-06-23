package kopia

import (
	"fmt"
	"testing"

	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/model"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/internal/version"
	"github.com/alcionai/corso/src/pkg/backup"
	"github.com/alcionai/corso/src/pkg/path"
)

func makeManifest(id, incmpl, bID string, reasons ...Reason) ManifestEntry {
	bIDKey, _ := makeTagKV(TagBackupID)

	return ManifestEntry{
		Manifest: &snapshot.Manifest{
			ID:               manifest.ID(id),
			IncompleteReason: incmpl,
			Tags:             map[string]string{bIDKey: bID},
		},
		Reasons: reasons,
	}
}

type BackupBasesUnitSuite struct {
	tester.Suite
}

func TestBackupBasesUnitSuite(t *testing.T) {
	suite.Run(t, &BackupBasesUnitSuite{Suite: tester.NewUnitSuite(t)})
}

func (suite *BackupBasesUnitSuite) TestMinBackupVersion() {
	table := []struct {
		name            string
		bb              *backupBases
		expectedVersion int
	}{
		{
			name:            "Nil BackupBase",
			expectedVersion: version.NoBackup,
		},
		{
			name:            "No Backups",
			bb:              &backupBases{},
			expectedVersion: version.NoBackup,
		},
		{
			name: "Unsorted Backups",
			bb: &backupBases{
				backups: []BackupEntry{
					{
						Backup: &backup.Backup{
							Version: 4,
						},
					},
					{
						Backup: &backup.Backup{
							Version: 0,
						},
					},
					{
						Backup: &backup.Backup{
							Version: 2,
						},
					},
				},
			},
			expectedVersion: 0,
		},
	}
	for _, test := range table {
		suite.Run(test.name, func() {
			assert.Equal(suite.T(), test.expectedVersion, test.bb.MinBackupVersion())
		})
	}
}

func (suite *BackupBasesUnitSuite) TestRemoveMergeBaseByManifestID() {
	backups := []BackupEntry{
		{Backup: &backup.Backup{SnapshotID: "1"}},
		{Backup: &backup.Backup{SnapshotID: "2"}},
		{Backup: &backup.Backup{SnapshotID: "3"}},
	}

	merges := []ManifestEntry{
		makeManifest("1", "", ""),
		makeManifest("2", "", ""),
		makeManifest("3", "", ""),
	}

	expected := &backupBases{
		backups:     []BackupEntry{backups[0], backups[1]},
		mergeBases:  []ManifestEntry{merges[0], merges[1]},
		assistBases: []ManifestEntry{merges[0], merges[1]},
	}

	delID := manifest.ID("3")

	table := []struct {
		name string
		// Below indices specify which items to add from the defined sets above.
		backup []int
		merge  []int
		assist []int
	}{
		{
			name:   "Not In Bases",
			backup: []int{0, 1},
			merge:  []int{0, 1},
			assist: []int{0, 1},
		},
		{
			name:   "Different Indexes",
			backup: []int{2, 0, 1},
			merge:  []int{0, 2, 1},
			assist: []int{0, 1, 2},
		},
		{
			name:   "First Item",
			backup: []int{2, 0, 1},
			merge:  []int{2, 0, 1},
			assist: []int{2, 0, 1},
		},
		{
			name:   "Middle Item",
			backup: []int{0, 2, 1},
			merge:  []int{0, 2, 1},
			assist: []int{0, 2, 1},
		},
		{
			name:   "Final Item",
			backup: []int{0, 1, 2},
			merge:  []int{0, 1, 2},
			assist: []int{0, 1, 2},
		},
		{
			name:   "Only In Backups",
			backup: []int{0, 1, 2},
			merge:  []int{0, 1},
			assist: []int{0, 1},
		},
		{
			name:   "Only In Merges",
			backup: []int{0, 1},
			merge:  []int{0, 1, 2},
			assist: []int{0, 1},
		},
		{
			name:   "Only In Assists",
			backup: []int{0, 1},
			merge:  []int{0, 1},
			assist: []int{0, 1, 2},
		},
	}

	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()
			bb := &backupBases{}

			for _, i := range test.backup {
				bb.backups = append(bb.backups, backups[i])
			}

			for _, i := range test.merge {
				bb.mergeBases = append(bb.mergeBases, merges[i])
			}

			for _, i := range test.assist {
				bb.assistBases = append(bb.assistBases, merges[i])
			}

			bb.RemoveMergeBaseByManifestID(delID)
			AssertBackupBasesEqual(t, expected, bb)
		})
	}
}

func (suite *BackupBasesUnitSuite) TestClearMergeBases() {
	bb := &backupBases{
		backups:    make([]BackupEntry, 2),
		mergeBases: make([]ManifestEntry, 2),
	}

	bb.ClearMergeBases()
	assert.Empty(suite.T(), bb.Backups())
	assert.Empty(suite.T(), bb.MergeBases())
}

func (suite *BackupBasesUnitSuite) TestClearAssistBases() {
	bb := &backupBases{assistBases: make([]ManifestEntry, 2)}

	bb.ClearAssistBases()
	assert.Empty(suite.T(), bb.AssistBases())
}

func (suite *BackupBasesUnitSuite) TestMergeBackupBases() {
	ro := "resource_owner"

	type testInput struct {
		id         int
		incomplete bool
		cat        []path.CategoryType
	}

	// Make a function so tests can modify things without messing with each other.
	makeBackupBases := func(ti []testInput) *backupBases {
		res := &backupBases{}

		for _, i := range ti {
			baseID := fmt.Sprintf("id%d", i.id)
			ir := ""

			if i.incomplete {
				ir = "checkpoint"
			}

			reasons := make([]Reason, 0, len(i.cat))

			for _, c := range i.cat {
				reasons = append(reasons, Reason{
					ResourceOwner: ro,
					Service:       path.ExchangeService,
					Category:      c,
				})
			}

			m := makeManifest(baseID, ir, "b"+baseID, reasons...)
			res.assistBases = append(res.assistBases, m)

			if i.incomplete {
				continue
			}

			b := BackupEntry{
				Backup: &backup.Backup{
					BaseModel:     model.BaseModel{ID: model.StableID("b" + baseID)},
					SnapshotID:    baseID,
					StreamStoreID: "ss" + baseID,
				},
				Reasons: reasons,
			}

			res.backups = append(res.backups, b)
			res.mergeBases = append(res.mergeBases, m)
		}

		return res
	}

	table := []struct {
		name   string
		bb     []testInput
		other  []testInput
		expect []testInput
	}{
		{
			name: "Other Empty",
			bb: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
		},
		{
			name: "BB Empty",
			other: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
		},
		{
			name: "Other overlaps Complete And Incomplete",
			bb: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			other: []testInput{
				{
					id:  2,
					cat: []path.CategoryType{path.EmailCategory},
				},
				{
					id:         3,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
		},
		{
			name: "Other Overlaps Complete",
			bb: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
			other: []testInput{
				{
					id:  2,
					cat: []path.CategoryType{path.EmailCategory},
				},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
			},
		},
		{
			name: "Other Overlaps Incomplete",
			bb: []testInput{
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			other: []testInput{
				{
					id:  2,
					cat: []path.CategoryType{path.EmailCategory},
				},
				{
					id:         3,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			expect: []testInput{
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
				{
					id:  2,
					cat: []path.CategoryType{path.EmailCategory},
				},
			},
		},
		{
			name: "Other Disjoint",
			bb: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			other: []testInput{
				{
					id:  2,
					cat: []path.CategoryType{path.ContactsCategory},
				},
				{
					id:         3,
					cat:        []path.CategoryType{path.ContactsCategory},
					incomplete: true,
				},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
				{
					id:  2,
					cat: []path.CategoryType{path.ContactsCategory},
				},
				{
					id:         3,
					cat:        []path.CategoryType{path.ContactsCategory},
					incomplete: true,
				},
			},
		},
		{
			name: "Other Reduced Reasons",
			bb: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
			},
			other: []testInput{
				{
					id: 2,
					cat: []path.CategoryType{
						path.EmailCategory,
						path.ContactsCategory,
					},
				},
				{
					id: 3,
					cat: []path.CategoryType{
						path.EmailCategory,
						path.ContactsCategory,
					},
					incomplete: true,
				},
			},
			expect: []testInput{
				{cat: []path.CategoryType{path.EmailCategory}},
				{
					id:         1,
					cat:        []path.CategoryType{path.EmailCategory},
					incomplete: true,
				},
				{
					id:  2,
					cat: []path.CategoryType{path.ContactsCategory},
				},
				{
					id:         3,
					cat:        []path.CategoryType{path.ContactsCategory},
					incomplete: true,
				},
			},
		},
	}

	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()

			bb := makeBackupBases(test.bb)
			other := makeBackupBases(test.other)
			expect := makeBackupBases(test.expect)

			ctx, flush := tester.NewContext(t)
			defer flush()

			got := bb.MergeBackupBases(
				ctx,
				other,
				func(reason Reason) string {
					return reason.Service.String() + reason.Category.String()
				})
			AssertBackupBasesEqual(t, expect, got)
		})
	}
}

func (suite *BackupBasesUnitSuite) TestFixupAndVerify() {
	ro := "resource_owner"

	makeMan := func(pct path.CategoryType, id, incmpl, bID string) ManifestEntry {
		reason := Reason{
			ResourceOwner: ro,
			Service:       path.ExchangeService,
			Category:      pct,
		}

		return makeManifest(id, incmpl, bID, reason)
	}

	// Make a function so tests can modify things without messing with each other.
	validMail1 := func() *backupBases {
		return &backupBases{
			backups: []BackupEntry{
				{
					Backup: &backup.Backup{
						BaseModel: model.BaseModel{
							ID: "bid1",
						},
						SnapshotID:    "id1",
						StreamStoreID: "ssid1",
					},
				},
			},
			mergeBases: []ManifestEntry{
				makeMan(path.EmailCategory, "id1", "", "bid1"),
			},
			assistBases: []ManifestEntry{
				makeMan(path.EmailCategory, "id1", "", "bid1"),
			},
		}
	}

	table := []struct {
		name   string
		bb     *backupBases
		expect BackupBases
	}{
		{
			name: "empty BaseBackups",
			bb:   &backupBases{},
		},
		{
			name: "Merge Base Without Backup",
			bb: func() *backupBases {
				res := validMail1()
				res.backups = nil

				return res
			}(),
		},
		{
			name: "Backup Missing Snapshot ID",
			bb: func() *backupBases {
				res := validMail1()
				res.backups[0].SnapshotID = ""

				return res
			}(),
		},
		{
			name: "Backup Missing Deets ID",
			bb: func() *backupBases {
				res := validMail1()
				res.backups[0].StreamStoreID = ""

				return res
			}(),
		},
		{
			name: "Incomplete Snapshot",
			bb: func() *backupBases {
				res := validMail1()
				res.mergeBases[0].IncompleteReason = "ir"
				res.assistBases[0].IncompleteReason = "ir"

				return res
			}(),
		},
		{
			name: "Duplicate Reason",
			bb: func() *backupBases {
				res := validMail1()
				res.mergeBases[0].Reasons = append(
					res.mergeBases[0].Reasons,
					res.mergeBases[0].Reasons[0])
				res.assistBases = res.mergeBases

				return res
			}(),
		},
		{
			name:   "Single Valid Entry",
			bb:     validMail1(),
			expect: validMail1(),
		},
		{
			name: "Single Valid Entry With Incomplete Assist With Same Reason",
			bb: func() *backupBases {
				res := validMail1()
				res.assistBases = append(
					res.assistBases,
					makeMan(path.EmailCategory, "id2", "checkpoint", "bid2"))

				return res
			}(),
			expect: func() *backupBases {
				res := validMail1()
				res.assistBases = append(
					res.assistBases,
					makeMan(path.EmailCategory, "id2", "checkpoint", "bid2"))

				return res
			}(),
		},
		{
			name: "Single Valid Entry With Backup With Old Deets ID",
			bb: func() *backupBases {
				res := validMail1()
				res.backups[0].DetailsID = res.backups[0].StreamStoreID
				res.backups[0].StreamStoreID = ""

				return res
			}(),
			expect: func() *backupBases {
				res := validMail1()
				res.backups[0].DetailsID = res.backups[0].StreamStoreID
				res.backups[0].StreamStoreID = ""

				return res
			}(),
		},
		{
			name: "Single Valid Entry With Multiple Reasons",
			bb: func() *backupBases {
				res := validMail1()
				res.mergeBases[0].Reasons = append(
					res.mergeBases[0].Reasons,
					Reason{
						ResourceOwner: ro,
						Service:       path.ExchangeService,
						Category:      path.ContactsCategory,
					})
				res.assistBases = res.mergeBases

				return res
			}(),
			expect: func() *backupBases {
				res := validMail1()
				res.mergeBases[0].Reasons = append(
					res.mergeBases[0].Reasons,
					Reason{
						ResourceOwner: ro,
						Service:       path.ExchangeService,
						Category:      path.ContactsCategory,
					})
				res.assistBases = res.mergeBases

				return res
			}(),
		},
		{
			name: "Two Entries Overlapping Reasons",
			bb: func() *backupBases {
				res := validMail1()
				res.mergeBases = append(
					res.mergeBases,
					makeMan(path.EmailCategory, "id2", "", "bid2"))
				res.assistBases = res.mergeBases

				return res
			}(),
		},
		{
			name: "Three Entries One Invalid",
			bb: func() *backupBases {
				res := validMail1()
				res.backups = append(
					res.backups,
					BackupEntry{
						Backup: &backup.Backup{
							BaseModel: model.BaseModel{
								ID: "bid2",
							},
						},
					},
					BackupEntry{
						Backup: &backup.Backup{
							BaseModel: model.BaseModel{
								ID: "bid3",
							},
							SnapshotID:    "id3",
							StreamStoreID: "ssid3",
						},
					})
				res.mergeBases = append(
					res.mergeBases,
					makeMan(path.ContactsCategory, "id2", "checkpoint", "bid2"),
					makeMan(path.EventsCategory, "id3", "", "bid3"))
				res.assistBases = res.mergeBases

				return res
			}(),
			expect: func() *backupBases {
				res := validMail1()
				res.backups = append(
					res.backups,
					BackupEntry{
						Backup: &backup.Backup{
							BaseModel: model.BaseModel{
								ID: "bid3",
							},
							SnapshotID:    "id3",
							StreamStoreID: "ssid3",
						},
					})
				res.mergeBases = append(
					res.mergeBases,
					makeMan(path.EventsCategory, "id3", "", "bid3"))
				res.assistBases = res.mergeBases

				return res
			}(),
		},
	}
	for _, test := range table {
		suite.Run(test.name, func() {
			ctx, flush := tester.NewContext(suite.T())
			defer flush()

			test.bb.fixupAndVerify(ctx)
			AssertBackupBasesEqual(suite.T(), test.expect, test.bb)
		})
	}
}