package kopia

import (
	"context"
	"sort"

	"github.com/kopia/kopia/repo/manifest"
	"github.com/kopia/kopia/snapshot"
	"github.com/pkg/errors"

	"github.com/alcionai/corso/src/pkg/logger"
	"github.com/alcionai/corso/src/pkg/path"
)

const (
	// Kopia does not do comparisons properly for empty tags right now so add some
	// placeholder value to them.
	defaultTagValue = "0"

	// Kopia CLI prefixes all user tags with "tag:"[1]. Maintaining this will
	// ensure we don't accidentally take reserved tags and that tags can be
	// displayed with kopia CLI.
	// (permalinks)
	// [1] https://github.com/kopia/kopia/blob/05e729a7858a6e86cb48ba29fb53cb6045efce2b/cli/command_snapshot_create.go#L169
	userTagPrefix = "tag:"
)

type snapshotManager interface {
	FindManifests(
		ctx context.Context,
		tags map[string]string,
	) ([]*manifest.EntryMetadata, error)
	LoadSnapshots(ctx context.Context, ids []manifest.ID) ([]*snapshot.Manifest, error)
}

type ownersCats struct {
	resourceOwners map[string]struct{}
	serviceCats    map[string]struct{}
}

func serviceCatTag(p path.Path) string {
	return p.Service().String() + p.Category().String()
}

func makeTagKV(k string) (string, string) {
	return userTagPrefix + k, defaultTagValue
}

// tagsFromStrings returns a map[string]string with tags for all ownersCats
// passed in. Currently uses placeholder values for each tag because there can
// be multiple instances of resource owners and categories in a single snapshot.
func tagsFromStrings(oc *ownersCats) map[string]string {
	res := make(map[string]string, len(oc.serviceCats)+len(oc.resourceOwners))

	for k := range oc.serviceCats {
		tk, tv := makeTagKV(k)
		res[tk] = tv
	}

	for k := range oc.resourceOwners {
		tk, tv := makeTagKV(k)
		res[tk] = tv
	}

	return res
}

// getLastIdx searches for manifests contained in both foundMans and metas
// and returns the most recent complete manifest index. If no complete manifest
// is in both lists returns -1.
func getLastIdx(
	foundMans map[manifest.ID]*snapshot.Manifest,
	metas []*manifest.EntryMetadata,
) int {
	// Minor optimization: the current code seems to return the entries from
	// earliest timestamp to latest (this is undocumented). Sort in the same
	// fashion so that we don't incur a bunch of swaps.
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].ModTime.Before(metas[j].ModTime)
	})

	// Search newest to oldest.
	for i := len(metas) - 1; i >= 0; i-- {
		m := foundMans[metas[i].ID]
		if m == nil || len(m.IncompleteReason) > 0 {
			continue
		}

		return i
	}

	return -1
}

// manifestsSinceLastComplete searches through mans and returns the most recent
// complete manifest (if one exists) and maybe the most recent incomplete
// manifest. If the newest incomplete manifest is more recent than the newest
// complete manifest then adds it to the returned list. Otherwise no incomplete
// manifest is returned. Returns nil if there are no complete or incomplete
// manifests in mans.
func manifestsSinceLastComplete(
	mans []*snapshot.Manifest,
) []*snapshot.Manifest {
	var (
		res             []*snapshot.Manifest
		foundIncomplete = false
	)

	// Manifests should maintain the sort order of the original IDs that were used
	// to fetch the data, but just in case sort oldest to newest.
	mans = snapshot.SortByTime(mans, false)

	for i := len(mans) - 1; i >= 0; i-- {
		m := mans[i]

		if len(m.IncompleteReason) > 0 {
			if !foundIncomplete {
				foundIncomplete = true

				res = append(res, m)
			}

			continue
		}

		// Once we find a complete snapshot we're done, even if we haven't
		// found an incomplete one yet.
		res = append(res, m)

		break
	}

	return res
}

// fetchPrevManifests returns the most recent, as-of-yet unfound complete and
// (maybe) incomplete manifests in metas. If the most recent incomplete manifest
// is older than the most recent complete manifest no incomplete manifest is
// returned. If only incomplete manifests exists, returns the most recent one.
// Returns no manifests if an error occurs.
func fetchPrevManifests(
	ctx context.Context,
	sm snapshotManager,
	foundMans map[manifest.ID]*snapshot.Manifest,
	tags map[string]string,
) ([]*snapshot.Manifest, error) {
	metas, err := sm.FindManifests(ctx, tags)
	if err != nil {
		return nil, errors.Wrap(err, "fetching manifest metas by tag")
	}

	if len(metas) == 0 {
		return nil, nil
	}

	lastCompleteIdx := getLastIdx(foundMans, metas)

	// We have a complete cached snapshot and it's the most recent. No need
	// to do anything else.
	if lastCompleteIdx == len(metas)-1 {
		return nil, nil
	}

	// TODO(ashmrtn): Remainder of the function can be simplified if we can inject
	// different tags to the snapshot checkpoints than the complete snapshot.

	// Fetch all manifests newer than the oldest complete snapshot. A little
	// wasteful as we may also re-fetch the most recent incomplete manifest, but
	// it reduces the complexity of returning the most recent incomplete manifest
	// if it is newer than the most recent complete manifest.
	ids := make([]manifest.ID, 0, len(metas)-(lastCompleteIdx+1))
	for i := lastCompleteIdx + 1; i < len(metas); i++ {
		ids = append(ids, metas[i].ID)
	}

	mans, err := sm.LoadSnapshots(ctx, ids)
	if err != nil {
		return nil, errors.Wrap(err, "fetching previous manifests")
	}

	return manifestsSinceLastComplete(mans), nil
}

// fetchPrevSnapshotManifests returns a set of manifests for complete and maybe
// incomplete snapshots for the given (resource owner, service, category)
// tuples. Up to two manifests can be returned per tuple: one complete and one
// incomplete. An incomplete manifest may be returned if it is newer than the
// newest complete manifest for the tuple. Manifests are deduped such that if
// multiple tuples match the same manifest it will only be returned once.
//
// TODO(ashmrtn): Use to get previous manifests so backup can find previously
// uploaded versions of a file.
func fetchPrevSnapshotManifests(
	ctx context.Context,
	sm snapshotManager,
	oc *ownersCats,
) []*snapshot.Manifest {
	mans := map[manifest.ID]*snapshot.Manifest{}

	// For each serviceCat/resource owner pair that we will be backing up, see if
	// there's a previous incomplete snapshot and/or a previous complete snapshot
	// we can pass in. Can be expanded to return more than the most recent
	// snapshots, but may require more memory at runtime.
	for serviceCat := range oc.serviceCats {
		serviceTagKey, serviceTagValue := makeTagKV(serviceCat)

		for resourceOwner := range oc.resourceOwners {
			resourceOwnerTagKey, resourceOwnerTagValue := makeTagKV(resourceOwner)

			tags := map[string]string{
				serviceTagKey:       serviceTagValue,
				resourceOwnerTagKey: resourceOwnerTagValue,
			}

			found, err := fetchPrevManifests(ctx, sm, mans, tags)
			if err != nil {
				logger.Ctx(ctx).Warnw(
					"fetching previous snapshot manifests for service/category/resource owner",
					"error",
					err,
					"service/category",
					serviceCat,
				)

				// Snapshot can still complete fine, just not as efficient.
				continue
			}

			// If we found more recent snapshots then add them.
			for _, m := range found {
				mans[m.ID] = m
			}
		}
	}

	res := make([]*snapshot.Manifest, 0, len(mans))
	for _, m := range mans {
		res = append(res, m)
	}

	return res
}