package taskstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// RootPage is a bounded recovery page. An empty NextCursor means traversal is complete.
type RootPage struct {
	Tasks      []Task
	NextCursor string
}

// TaskPage is a bounded caller/root-scoped page, including the root itself.
type TaskPage struct {
	Tasks      []Task
	NextCursor string
}

type taskCursor struct {
	Version        int
	Scope          Scope
	After, Through int64
}

func encodeTaskCursor(c taskCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ListTasksForRecovery is for the trusted Controller only, never Agent input.
// Pass NextCursor unchanged with the same trusted scope. Like root recovery,
// insertion-order pages include every task present at the initial watermark once,
// exclude later inserts, and survive reopening the same database. Metadata is
// read per page, not frozen. Cursors are not authorization tokens. External DB
// modification invalidates traversal guarantees. Before stopping a task tree the
// Controller must separately block new delegation: this read does not freeze it.
func (s *Store) ListTasksForRecovery(ctx context.Context, scope Scope, cursor string, limit int) (TaskPage, error) {
	if err := s.check(ctx); err != nil {
		return TaskPage{}, err
	}
	if !validScope(scope) || limit < 1 || limit > 500 || len(cursor) > 8192 {
		return TaskPage{}, fault(Invalid, "scope and limit 1..500 required")
	}
	c := taskCursor{Version: 1, Scope: scope}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &c) != nil || encodeTaskCursor(c) != cursor || c.Version != 1 || c.Scope != scope || c.After < 1 || c.Through <= c.After {
			return TaskPage{}, fault(Invalid, "invalid task recovery cursor")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskPage{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	if cursor == "" {
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM tasks WHERE caller=? AND root_id=?", scope.CallerID, scope.RootID).Scan(&c.Through)
	} else {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE caller=? AND root_id=? AND rowid IN (?,?)", scope.CallerID, scope.RootID, c.After, c.Through).Scan(&count)
		if err == nil && count != 2 {
			return TaskPage{}, fault(Invalid, "task recovery cursor no longer valid")
		}
	}
	if err != nil {
		return TaskPage{}, s.normalize(ctx, err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND root_id=? AND rowid>? AND rowid<=? ORDER BY rowid LIMIT ?", scope.CallerID, scope.RootID, c.After, c.Through, limit+1)
	if err != nil {
		return TaskPage{}, s.normalize(ctx, err)
	}
	page := TaskPage{Tasks: []Task{}}
	for rows.Next() {
		t, e := scan(rows)
		if e != nil {
			rows.Close()
			return TaskPage{}, s.normalize(ctx, e)
		}
		page.Tasks = append(page.Tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return TaskPage{}, s.normalize(ctx, err)
	}
	if len(page.Tasks) > limit {
		page.Tasks = page.Tasks[:limit]
		err = tx.QueryRowContext(ctx, "SELECT rowid FROM tasks WHERE id=?", page.Tasks[limit-1].ID).Scan(&c.After)
		if err != nil {
			return TaskPage{}, s.normalize(ctx, err)
		}
		page.NextCursor = encodeTaskCursor(c)
	}
	if err = tx.Commit(); err != nil {
		return TaskPage{}, s.normalize(ctx, err)
	}
	return page, nil
}

type rootCursor struct {
	Version        int
	Caller         string
	After, Through int64
}

func encodeRootCursor(c rootCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ListRootsForRecovery is for the trusted Controller only. Never expose it to
// Agent environments: CallerID must be obtained from trusted identity, not input.
// Unlike Get/List, it discovers roots without a previously known root ID.
//
// Pass an empty cursor to begin, then pass NextCursor unchanged with the same
// caller. Pages use SQLite insertion order and fix an upper insertion watermark
// on the first call. Every root present then is returned once; later inserts are
// excluded, even if they sort before an earlier task ID. State/metadata reflects
// each page's transaction, not a snapshot across calls. Task deletion is not
// supported by this Store; external database modification invalidates this
// guarantee. Cursors survive Close/reopen of the same database, are not secrets
// or authorization tokens, and must not be reused against a different database.
func (s *Store) ListRootsForRecovery(ctx context.Context, callerID, cursor string, limit int) (RootPage, error) {
	if err := s.check(ctx); err != nil {
		return RootPage{}, err
	}
	if strings.TrimSpace(callerID) == "" || limit < 1 || limit > 500 || len(cursor) > 8192 {
		return RootPage{}, fault(Invalid, "caller and limit 1..500 required")
	}
	c := rootCursor{Version: 1, Caller: callerID}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &c) != nil || encodeRootCursor(c) != cursor || c.Version != 1 || c.Caller != callerID || c.After < 1 || c.Through <= c.After {
			return RootPage{}, fault(Invalid, "invalid recovery cursor")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RootPage{}, s.normalize(ctx, err)
	}
	defer tx.Rollback()
	if cursor == "" {
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM tasks WHERE caller=? AND parent_id IS NULL", callerID).Scan(&c.Through)
	} else {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE caller=? AND parent_id IS NULL AND rowid IN (?,?)", callerID, c.After, c.Through).Scan(&count)
		if err == nil && count != 2 {
			return RootPage{}, fault(Invalid, "recovery cursor no longer valid")
		}
	}
	if err != nil {
		return RootPage{}, s.normalize(ctx, err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE caller=? AND parent_id IS NULL AND rowid>? AND rowid<=? ORDER BY rowid LIMIT ?", callerID, c.After, c.Through, limit+1)
	if err != nil {
		return RootPage{}, s.normalize(ctx, err)
	}
	page := RootPage{Tasks: []Task{}}
	for rows.Next() {
		t, e := scan(rows)
		if e != nil {
			rows.Close()
			return RootPage{}, s.normalize(ctx, e)
		}
		page.Tasks = append(page.Tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RootPage{}, s.normalize(ctx, err)
	}
	if len(page.Tasks) > limit {
		page.Tasks = page.Tasks[:limit]
		err = tx.QueryRowContext(ctx, "SELECT rowid FROM tasks WHERE id=?", page.Tasks[limit-1].ID).Scan(&c.After)
		if err != nil {
			return RootPage{}, s.normalize(ctx, err)
		}
		page.NextCursor = encodeRootCursor(c)
	}
	if err = tx.Commit(); err != nil {
		return RootPage{}, s.normalize(ctx, err)
	}
	return page, nil
}
