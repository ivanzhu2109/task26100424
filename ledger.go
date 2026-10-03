// Package archiveretention implements an in-memory retention ledger for
// archived versions. It tracks per-volume retention policies and per-version
// retention promises; it never touches file contents.
package archiveretention

import "sort"

// SecondsPerDay is the fixed length of a day used for retention math.
const SecondsPerDay int64 = 86400

// VersionStatus is the result of querying a version's retention state.
type VersionStatus struct {
	// EffectiveDeadline is RegisteredAt + PromisedDays*SecondsPerDay.
	EffectiveDeadline int64
	// Clearable reports whether the version may be cleared at the query time.
	Clearable bool
}

// version records a single registered version's promise.
type version struct {
	id           string
	volume       string
	registeredAt int64
	promisedDays int64
}

// volume holds a volume's current policy.
type volume struct {
	retentionDays int64
	legalHold     bool
}

// Ledger is the in-memory retention ledger.
type Ledger struct {
	versions map[string]*version
	volumes  map[string]*volume
}

// New creates an empty Ledger.
func New() *Ledger {
	return &Ledger{
		versions: make(map[string]*version),
		volumes:  make(map[string]*volume),
	}
}

// SubmitVolumePolicy sets (or replaces) a volume's policy.
func (l *Ledger) SubmitVolumePolicy(now int64, volumeID string, retentionDays int64, legalHold bool) {
	vol, ok := l.volumes[volumeID]
	if !ok {
		vol = &volume{}
		l.volumes[volumeID] = vol
	}
	vol.retentionDays = retentionDays
	vol.legalHold = legalHold
	// Raising retention extends every non-cleared version's promise to at
	// least the new value; lowering never revokes existing promises.
	for _, v := range l.versions {
		if v.volume == volumeID && v.promisedDays < retentionDays {
			v.promisedDays = retentionDays
		}
	}
}

// RegisterVersion registers a new version under its volume's current policy.
func (l *Ledger) RegisterVersion(now int64, versionID string, volumeID string) {
	days := int64(0)
	if vol, ok := l.volumes[volumeID]; ok {
		days = vol.retentionDays
	}
	l.versions[versionID] = &version{
		id:           versionID,
		volume:       volumeID,
		registeredAt: now,
		promisedDays: days,
	}
}

// QueryVersion returns the version's current status.
func (l *Ledger) QueryVersion(now int64, versionID string) (VersionStatus, bool) {
	v, ok := l.versions[versionID]
	if !ok {
		return VersionStatus{}, false
	}
	deadline := v.registeredAt + v.promisedDays*SecondsPerDay
	held := false
	if vol, ok := l.volumes[v.volume]; ok {
		held = vol.legalHold
	}
	return VersionStatus{
		EffectiveDeadline: deadline,
		Clearable:         now >= deadline && !held,
	}, true
}

// ClearExpired removes and returns the IDs of up to maxCount expired,
// non-frozen versions, earliest effective deadline first, ties broken by ID.
func (l *Ledger) ClearExpired(now int64, maxCount int) []string {
	if maxCount <= 0 {
		return nil
	}
	type candidate struct {
		id       string
		deadline int64
	}
	var expired []candidate
	for _, v := range l.versions {
		if vol, ok := l.volumes[v.volume]; ok && vol.legalHold {
			continue
		}
		deadline := v.registeredAt + v.promisedDays*SecondsPerDay
		if now >= deadline {
			expired = append(expired, candidate{id: v.id, deadline: deadline})
		}
	}
	sort.Slice(expired, func(i, j int) bool {
		if expired[i].deadline != expired[j].deadline {
			return expired[i].deadline < expired[j].deadline
		}
		return expired[i].id < expired[j].id
	})
	if len(expired) > maxCount {
		expired = expired[:maxCount]
	}
	cleared := make([]string, 0, len(expired))
	for _, c := range expired {
		delete(l.versions, c.id)
		cleared = append(cleared, c.id)
	}
	return cleared
}
