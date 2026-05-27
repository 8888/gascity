package beads

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	bbolt "go.etcd.io/bbolt"
)

var (
	bboltBucketRecords = []byte("records")
	bboltBucketWisps   = []byte("wisps")
	bboltBucketDeps    = []byte("deps")
	bboltBucketMeta    = []byte("meta")
	bboltMetaSeq       = []byte("seq")
)

var sharedBboltStores = struct {
	sync.Mutex
	stores map[string]*BboltStore
}{}

// BboltStore is a write-through bbolt-backed Store implementation.
//
// Mutations commit to bbolt before the in-memory hot index is updated. Reads
// are served from the hot index populated at OpenBboltStore time and updated
// after each successful write.
type BboltStore struct {
	mu sync.RWMutex

	db     *bbolt.DB
	path   string
	prefix string
	seq    int
	closed bool

	main      map[string]Bead
	wisps     map[string]Bead
	order     []string
	orderSeen map[string]bool
	deps      []Dep
}

type bboltStoreOptions struct {
	prefix string
}

// BboltStoreOption customizes OpenBboltStore.
type BboltStoreOption func(*bboltStoreOptions)

// WithBboltStoreIDPrefix sets the generated ID prefix. Empty keeps the default.
func WithBboltStoreIDPrefix(prefix string) BboltStoreOption {
	return func(o *bboltStoreOptions) {
		if prefix != "" {
			o.prefix = prefix
		}
	}
}

// BboltStorePath returns the scoped bbolt store path for a city or rig root.
func BboltStorePath(scopeRoot string) string {
	return filepath.Join(scopeRoot, ".gc", "state", "bbolt", "beads.bolt")
}

// OpenBboltStore opens or creates a bbolt-backed bead store at path.
func OpenBboltStore(path string, opts ...BboltStoreOption) (*BboltStore, error) {
	return openBboltStoreUncached(path, opts...)
}

// OpenSharedBboltStore opens a process-shared bbolt-backed bead store at path.
//
// bbolt enforces an exclusive writer lock per process. Long-running gc
// processes open the same store from several command/controller surfaces, so
// this helper returns the already-open handle for a path instead of contending
// with itself.
func OpenSharedBboltStore(path string, opts ...BboltStoreOption) (*BboltStore, error) {
	key := normalizeBboltPath(path)
	cfg := resolveBboltStoreOptions(opts...)

	sharedBboltStores.Lock()
	defer sharedBboltStores.Unlock()
	if sharedBboltStores.stores != nil {
		if existing := sharedBboltStores.stores[key]; existing != nil {
			existing.mu.Lock()
			if !existing.closed {
				existing.prefix = cfg.prefix
				existing.mu.Unlock()
				return existing, nil
			}
			existing.mu.Unlock()
			delete(sharedBboltStores.stores, key)
		}
	} else {
		sharedBboltStores.stores = make(map[string]*BboltStore)
	}
	store, err := openBboltStoreUncached(key, opts...)
	if err != nil {
		return nil, err
	}
	sharedBboltStores.stores[key] = store
	return store, nil
}

func openBboltStoreUncached(path string, opts ...BboltStoreOption) (*BboltStore, error) {
	path = normalizeBboltPath(path)
	cfg := resolveBboltStoreOptions(opts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("bbolt: create store directory: %w", err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("bbolt: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{bboltBucketRecords, bboltBucketWisps, bboltBucketDeps, bboltBucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("bbolt: create buckets: %w", err)
	}

	s := &BboltStore{
		db:     db,
		path:   path,
		prefix: cfg.prefix,
	}
	s.resetCoreLocked()
	if err := s.load(); err != nil {
		db.Close() //nolint:errcheck
		return nil, err
	}
	return s, nil
}

func resolveBboltStoreOptions(opts ...BboltStoreOption) bboltStoreOptions {
	cfg := bboltStoreOptions{prefix: "gc"}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func normalizeBboltPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

// Shutdown closes the underlying bbolt database handle. It is idempotent.
func (s *BboltStore) Shutdown() error {
	sharedBboltStores.Lock()
	defer sharedBboltStores.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if sharedBboltStores.stores != nil && sharedBboltStores.stores[s.path] == s {
		delete(sharedBboltStores.stores, s.path)
	}
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Create persists a new bead.
func (s *BboltStore) Create(b Bead) (Bead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return Bead{}, err
	}

	stored := s.normalizeCreateLocked(b)
	if _, ok := s.findLocked(stored.ID); ok {
		return Bead{}, fmt.Errorf("bbolt: creating bead %q: duplicate id", stored.ID)
	}
	newDeps := normalizeBboltDeps(depsFromNeeds(stored))
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		if err := s.putBeadTx(tx, stored); err != nil {
			return err
		}
		if err := s.putSeqTx(tx); err != nil {
			return err
		}
		for _, dep := range newDeps {
			if err := s.putDepTx(tx, dep); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return Bead{}, fmt.Errorf("bbolt: creating bead %q: %w", stored.ID, err)
	}
	s.upsertOwnedLocked(stored)
	for _, dep := range newDeps {
		s.depAddCoreLocked(dep.IssueID, dep.DependsOnID, dep.Type)
	}
	return cloneBead(stored), nil
}

// Get retrieves a bead by ID.
func (s *BboltStore) Get(id string) (Bead, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.findLocked(id); ok {
		return b, nil
	}
	return Bead{}, fmt.Errorf("bbolt: getting bead %q: %w", id, ErrNotFound)
}

// Update modifies fields of an existing bead.
func (s *BboltStore) Update(id string, opts UpdateOpts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	b, ok := s.findLocked(id)
	if !ok {
		return fmt.Errorf("bbolt: updating bead %q: %w", id, ErrNotFound)
	}
	wasClosed := b.Status == "closed"
	applyHQUpdate(&b, opts)
	if opts.Status != nil {
		switch {
		case b.Status == "closed" && !wasClosed:
			hqStampClosedAt(&b, time.Now())
		case b.Status != "closed" && wasClosed:
			hqClearClosedAt(&b)
		}
	}
	if err := s.persistBeadLocked(b); err != nil {
		return fmt.Errorf("bbolt: updating bead %q: %w", id, err)
	}
	s.upsertOwnedLocked(b)
	return nil
}

// Close sets a bead's status to closed.
func (s *BboltStore) Close(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	b, ok := s.findLocked(id)
	if !ok {
		return fmt.Errorf("bbolt: closing bead %q: %w", id, ErrNotFound)
	}
	if b.Status == "closed" {
		return nil
	}
	b.Status = "closed"
	hqStampClosedAt(&b, time.Now())
	if err := s.persistBeadLocked(b); err != nil {
		return fmt.Errorf("bbolt: closing bead %q: %w", id, err)
	}
	s.upsertOwnedLocked(b)
	return nil
}

// Reopen sets a closed bead's status back to open.
func (s *BboltStore) Reopen(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	b, ok := s.findLocked(id)
	if !ok {
		return fmt.Errorf("bbolt: reopening bead %q: %w", id, ErrNotFound)
	}
	if b.Status == "open" {
		return nil
	}
	b.Status = "open"
	hqClearClosedAt(&b)
	if err := s.persistBeadLocked(b); err != nil {
		return fmt.Errorf("bbolt: reopening bead %q: %w", id, err)
	}
	s.upsertOwnedLocked(b)
	return nil
}

// CloseAll closes multiple beads and applies metadata to each closed bead.
func (s *BboltStore) CloseAll(ids []string, metadata map[string]string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return 0, err
	}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	changed := make([]Bead, 0, len(idSet))
	for id := range idSet {
		b, ok := s.findLocked(id)
		if !ok || b.Status == "closed" {
			continue
		}
		b.Status = "closed"
		hqStampClosedAt(&b, time.Now())
		if len(metadata) > 0 {
			if b.Metadata == nil {
				b.Metadata = make(map[string]string, len(metadata))
			}
			for k, v := range metadata {
				b.Metadata[k] = v
			}
		}
		changed = append(changed, b)
	}
	if len(changed) == 0 {
		return 0, nil
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		for _, b := range changed {
			if err := s.putBeadTx(tx, b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("bbolt: close all: %w", err)
	}
	for _, b := range changed {
		s.upsertOwnedLocked(b)
	}
	return len(changed), nil
}

// List returns beads matching the query.
func (s *BboltStore) List(query ListQuery) ([]Bead, error) {
	if !query.HasFilter() && !query.AllowScan {
		return nil, fmt.Errorf("bbolt: listing beads: %w", ErrQueryRequiresScan)
	}
	s.mu.RLock()
	snapshot := s.snapshotBeadsLocked(query.TierMode)
	s.mu.RUnlock()
	return applyListQuery(snapshot, query), nil
}

// ListOpen returns non-closed beads in creation order by default.
func (s *BboltStore) ListOpen(status ...string) ([]Bead, error) {
	query := ListQuery{AllowScan: true}
	if len(status) > 0 {
		query.Status = status[0]
	}
	return s.List(query)
}

// Ready returns all open, unblocked actionable main-tier beads.
func (s *BboltStore) Ready(query ...ReadyQuery) ([]Bead, error) {
	q := readyQueryFromArgs(query)
	s.mu.RLock()
	statusByID := make(map[string]string, len(s.main)+len(s.wisps))
	for id, bead := range s.main {
		statusByID[id] = bead.Status
	}
	for id, bead := range s.wisps {
		statusByID[id] = bead.Status
	}
	deps := snapshotHQDeps(s.deps)
	candidates := make([]Bead, 0, len(s.main))
	for _, id := range s.order {
		b, ok := s.main[id]
		if !ok {
			continue
		}
		candidates = append(candidates, cloneBead(b))
	}
	s.mu.RUnlock()

	result := make([]Bead, 0, len(candidates))
	for _, b := range candidates {
		if b.Status != "open" {
			continue
		}
		if q.Assignee != "" && b.Assignee != q.Assignee {
			continue
		}
		if IsReadyExcludedType(b.Type) || hqBlockedBySnapshot(b.ID, deps, statusByID) {
			continue
		}
		result = append(result, b)
		if q.Limit > 0 && len(result) >= q.Limit {
			break
		}
	}
	return result, nil
}

// Children returns children of parentID.
func (s *BboltStore) Children(parentID string, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		ParentID:      parentID,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedAsc,
	})
}

// ListByLabel returns beads matching a label.
func (s *BboltStore) ListByLabel(label string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		Label:         label,
		Limit:         limit,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedDesc,
		TierMode:      TierModeFromOpts(opts),
	})
}

// ListByAssignee returns beads assigned to assignee with status.
func (s *BboltStore) ListByAssignee(assignee, status string, limit int) ([]Bead, error) {
	return s.List(ListQuery{
		Assignee: assignee,
		Status:   status,
		Limit:    limit,
		Sort:     SortCreatedDesc,
	})
}

// ListByMetadata returns beads whose metadata contains all filters.
func (s *BboltStore) ListByMetadata(filters map[string]string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		Metadata:      filters,
		Limit:         limit,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedDesc,
		TierMode:      TierModeFromOpts(opts),
	})
}

// SetMetadata sets a single metadata key-value pair.
func (s *BboltStore) SetMetadata(id, key, value string) error {
	return s.SetMetadataBatch(id, map[string]string{key: value})
}

// SetMetadataBatch atomically merges metadata into a bead.
func (s *BboltStore) SetMetadataBatch(id string, kvs map[string]string) error {
	if len(kvs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	b, ok := s.findLocked(id)
	if !ok {
		return fmt.Errorf("bbolt: setting metadata batch on %q: %w", id, ErrNotFound)
	}
	if b.Metadata == nil {
		b.Metadata = make(map[string]string, len(kvs))
	}
	for k, v := range kvs {
		b.Metadata[k] = v
	}
	if err := s.persistBeadLocked(b); err != nil {
		return fmt.Errorf("bbolt: setting metadata batch on %q: %w", id, err)
	}
	s.upsertOwnedLocked(b)
	return nil
}

// Tx executes fn against the BboltStore write surface.
func (s *BboltStore) Tx(_ string, fn func(tx Tx) error) error {
	return runSequentialTx(s, fn)
}

// Delete permanently removes a bead and dependency edges touching it.
func (s *BboltStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if _, ok := s.findLocked(id); !ok {
		return fmt.Errorf("bbolt: deleting bead %q: %w", id, ErrNotFound)
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.Bucket(bboltBucketRecords).Delete([]byte(id)); err != nil {
			return err
		}
		if err := tx.Bucket(bboltBucketWisps).Delete([]byte(id)); err != nil {
			return err
		}
		return s.deleteDepsTouchingTx(tx, id)
	}); err != nil {
		return fmt.Errorf("bbolt: deleting bead %q: %w", id, err)
	}
	s.deleteLocked(id)
	return nil
}

// Ping verifies that the bbolt file is accessible.
func (s *BboltStore) Ping() error {
	s.mu.RLock()
	db := s.db
	closed := s.closed
	s.mu.RUnlock()
	if closed || db == nil {
		return fmt.Errorf("bbolt: database is closed")
	}
	if err := db.View(func(tx *bbolt.Tx) error {
		if tx.Bucket(bboltBucketMeta) == nil {
			return fmt.Errorf("missing meta bucket")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("bbolt: ping: %w", err)
	}
	return nil
}

// DepAdd records a dependency.
func (s *BboltStore) DepAdd(issueID, dependsOnID, depType string) error {
	if depType == "" {
		depType = "blocks"
	}
	dep := Dep{IssueID: issueID, DependsOnID: dependsOnID, Type: depType}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	for _, existing := range s.deps {
		if existing == dep {
			return nil
		}
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		if depType != "parent-child" {
			if err := s.deleteNonParentDepsForPairTx(tx, issueID, dependsOnID); err != nil {
				return err
			}
		}
		return s.putDepTx(tx, dep)
	}); err != nil {
		return fmt.Errorf("bbolt: adding dependency %q -> %q: %w", issueID, dependsOnID, err)
	}
	s.depAddCoreLocked(issueID, dependsOnID, depType)
	return nil
}

// DepRemove removes a dependency between two beads.
func (s *BboltStore) DepRemove(issueID, dependsOnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpenLocked(); err != nil {
		return err
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		return s.deleteDepsForPairTx(tx, issueID, dependsOnID)
	}); err != nil {
		return fmt.Errorf("bbolt: removing dependency %q -> %q: %w", issueID, dependsOnID, err)
	}
	s.depRemoveCoreLocked(issueID, dependsOnID)
	return nil
}

// DepList returns dependencies in the requested direction.
func (s *BboltStore) DepList(id, direction string) ([]Dep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Dep
	for _, d := range s.deps {
		switch direction {
		case "up":
			if d.DependsOnID == id {
				result = append(result, d)
			}
		default:
			if d.IssueID == id {
				result = append(result, d)
			}
		}
	}
	return result, nil
}

func (s *BboltStore) resetCoreLocked() {
	s.main = make(map[string]Bead)
	s.wisps = make(map[string]Bead)
	s.order = nil
	s.orderSeen = make(map[string]bool)
	s.deps = nil
	s.seq = 0
}

func (s *BboltStore) load() error {
	var loaded []Bead
	var deps []Dep
	seq := 0
	err := s.db.View(func(tx *bbolt.Tx) error {
		for _, spec := range []struct {
			name      []byte
			ephemeral bool
		}{
			{name: bboltBucketRecords},
			{name: bboltBucketWisps, ephemeral: true},
		} {
			bucket := tx.Bucket(spec.name)
			if bucket == nil {
				continue
			}
			if err := bucket.ForEach(func(_, value []byte) error {
				var bead Bead
				if err := json.Unmarshal(value, &bead); err != nil {
					return err
				}
				bead.Ephemeral = spec.ephemeral
				loaded = append(loaded, bead)
				return nil
			}); err != nil {
				return err
			}
		}
		if bucket := tx.Bucket(bboltBucketDeps); bucket != nil {
			if err := bucket.ForEach(func(_, value []byte) error {
				var dep Dep
				if err := json.Unmarshal(value, &dep); err != nil {
					return err
				}
				deps = append(deps, dep)
				return nil
			}); err != nil {
				return err
			}
		}
		if bucket := tx.Bucket(bboltBucketMeta); bucket != nil {
			if raw := bucket.Get(bboltMetaSeq); len(raw) == 8 {
				seq = int(binary.LittleEndian.Uint64(raw))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("bbolt: load: %w", err)
	}

	sort.SliceStable(loaded, func(i, j int) bool {
		if loaded[i].CreatedAt.Equal(loaded[j].CreatedAt) {
			return loaded[i].ID < loaded[j].ID
		}
		return loaded[i].CreatedAt.Before(loaded[j].CreatedAt)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetCoreLocked()
	s.seq = seq
	for _, bead := range loaded {
		s.upsertOwnedLocked(cloneBead(bead))
	}
	s.deps = normalizeBboltDeps(deps)
	return nil
}

func (s *BboltStore) ensureOpenLocked() error {
	if s.closed || s.db == nil {
		return fmt.Errorf("bbolt: database is closed")
	}
	return nil
}

func (s *BboltStore) normalizeCreateLocked(b Bead) Bead {
	b = cloneBead(b)
	if b.ID == "" {
		s.seq++
		b.ID = fmt.Sprintf("%s-%d", s.prefix, s.seq)
	} else if n := numericIDSuffix(b.ID); n > s.seq {
		s.seq = n
	}
	if b.Status == "" {
		b.Status = "open"
	}
	if b.Type == "" {
		b.Type = "task"
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	return b
}

func (s *BboltStore) findLocked(id string) (Bead, bool) {
	if b, ok := s.main[id]; ok {
		return cloneBead(b), true
	}
	if b, ok := s.wisps[id]; ok {
		return cloneBead(b), true
	}
	return Bead{}, false
}

func (s *BboltStore) upsertOwnedLocked(b Bead) {
	delete(s.main, b.ID)
	delete(s.wisps, b.ID)
	if !s.orderSeen[b.ID] {
		s.order = append(s.order, b.ID)
		s.orderSeen[b.ID] = true
	}
	if n := numericIDSuffix(b.ID); n > s.seq {
		s.seq = n
	}
	if b.Ephemeral {
		s.wisps[b.ID] = cloneBead(b)
		return
	}
	s.main[b.ID] = cloneBead(b)
}

func (s *BboltStore) deleteLocked(id string) {
	delete(s.main, id)
	delete(s.wisps, id)
	filtered := s.deps[:0]
	for _, dep := range s.deps {
		if dep.IssueID == id || dep.DependsOnID == id {
			continue
		}
		filtered = append(filtered, dep)
	}
	s.deps = filtered
}

func (s *BboltStore) snapshotBeadsLocked(tier TierMode) []Bead {
	out := make([]Bead, 0, len(s.main)+len(s.wisps))
	seen := make(map[string]bool, len(s.order))
	for _, id := range s.order {
		seen[id] = true
		switch tier {
		case TierWisps:
			if b, ok := s.wisps[id]; ok {
				out = append(out, cloneBead(b))
			}
		case TierBoth:
			if b, ok := s.main[id]; ok {
				out = append(out, cloneBead(b))
			} else if b, ok := s.wisps[id]; ok {
				out = append(out, cloneBead(b))
			}
		default:
			if b, ok := s.main[id]; ok {
				out = append(out, cloneBead(b))
			}
		}
	}
	appendMissing := func(items map[string]Bead) {
		for id, bead := range items {
			if seen[id] {
				continue
			}
			out = append(out, cloneBead(bead))
		}
	}
	switch tier {
	case TierWisps:
		appendMissing(s.wisps)
	case TierBoth:
		appendMissing(s.main)
		appendMissing(s.wisps)
	default:
		appendMissing(s.main)
	}
	return out
}

func (s *BboltStore) persistBeadLocked(b Bead) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return s.putBeadTx(tx, b)
	})
}

func (s *BboltStore) putBeadTx(tx *bbolt.Tx, b Bead) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	dst := bboltBucketRecords
	other := bboltBucketWisps
	if b.Ephemeral {
		dst = bboltBucketWisps
		other = bboltBucketRecords
	}
	if err := tx.Bucket(other).Delete([]byte(b.ID)); err != nil {
		return err
	}
	return tx.Bucket(dst).Put([]byte(b.ID), data)
}

func (s *BboltStore) putSeqTx(tx *bbolt.Tx) error {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], uint64(s.seq))
	return tx.Bucket(bboltBucketMeta).Put(bboltMetaSeq, data[:])
}

func (s *BboltStore) putDepTx(tx *bbolt.Tx, dep Dep) error {
	data, err := json.Marshal(dep)
	if err != nil {
		return err
	}
	return tx.Bucket(bboltBucketDeps).Put([]byte(bboltDepKey(dep)), data)
}

func (s *BboltStore) deleteDepsTouchingTx(tx *bbolt.Tx, id string) error {
	bucket := tx.Bucket(bboltBucketDeps)
	var keys [][]byte
	if err := bucket.ForEach(func(key, value []byte) error {
		var dep Dep
		if err := json.Unmarshal(value, &dep); err != nil {
			return err
		}
		if dep.IssueID == id || dep.DependsOnID == id {
			keys = append(keys, append([]byte(nil), key...))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *BboltStore) deleteDepsForPairTx(tx *bbolt.Tx, issueID, dependsOnID string) error {
	bucket := tx.Bucket(bboltBucketDeps)
	var keys [][]byte
	if err := bucket.ForEach(func(key, value []byte) error {
		var dep Dep
		if err := json.Unmarshal(value, &dep); err != nil {
			return err
		}
		if dep.IssueID == issueID && dep.DependsOnID == dependsOnID {
			keys = append(keys, append([]byte(nil), key...))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *BboltStore) deleteNonParentDepsForPairTx(tx *bbolt.Tx, issueID, dependsOnID string) error {
	bucket := tx.Bucket(bboltBucketDeps)
	var keys [][]byte
	if err := bucket.ForEach(func(key, value []byte) error {
		var dep Dep
		if err := json.Unmarshal(value, &dep); err != nil {
			return err
		}
		if dep.IssueID == issueID && dep.DependsOnID == dependsOnID && dep.Type != "parent-child" {
			keys = append(keys, append([]byte(nil), key...))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *BboltStore) depAddCoreLocked(issueID, dependsOnID, depType string) {
	if depType == "" {
		depType = "blocks"
	}
	for i, d := range s.deps {
		if d.IssueID == issueID && d.DependsOnID == dependsOnID && d.Type == depType {
			return
		}
		if d.IssueID == issueID && d.DependsOnID == dependsOnID && d.Type != "parent-child" && depType != "parent-child" {
			s.deps[i].Type = depType
			return
		}
	}
	s.deps = append(s.deps, Dep{IssueID: issueID, DependsOnID: dependsOnID, Type: depType})
}

func (s *BboltStore) depRemoveCoreLocked(issueID, dependsOnID string) {
	filtered := s.deps[:0]
	for _, d := range s.deps {
		if d.IssueID == issueID && d.DependsOnID == dependsOnID {
			continue
		}
		filtered = append(filtered, d)
	}
	s.deps = filtered
}

func bboltDepKey(dep Dep) string {
	return dep.IssueID + "\x00" + dep.DependsOnID + "\x00" + dep.Type
}

func normalizeBboltDeps(in []Dep) []Dep {
	out := make([]Dep, 0, len(in))
	for _, dep := range in {
		if dep.Type == "" {
			dep.Type = "blocks"
		}
		handled := false
		for i, existing := range out {
			if existing.IssueID == dep.IssueID && existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
				handled = true
				break
			}
			if existing.IssueID == dep.IssueID && existing.DependsOnID == dep.DependsOnID && existing.Type != "parent-child" && dep.Type != "parent-child" {
				out[i].Type = dep.Type
				handled = true
				break
			}
		}
		if !handled {
			out = append(out, dep)
		}
	}
	return out
}

var _ Store = (*BboltStore)(nil)
