package taskstore

import (
	"context"
	"database/sql"
	"errors"
)

// RevokeRoot permanently denies gateway capabilities for an owned root and its
// entire tree. This trusted control-plane API does not stop runtime resources,
// change task states, or persist credentials. There is deliberately no undo API.
func (s *Store) RevokeRoot(ctx context.Context, scope Scope) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if !validScope(scope) {
		return fault(Invalid, "caller and root required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO root_revocations(root_id,caller)
 SELECT id,caller FROM tasks WHERE id=? AND root_id=? AND caller=? AND parent_id IS NULL
 ON CONFLICT(root_id) DO UPDATE SET caller=excluded.caller`, scope.RootID, scope.RootID, scope.CallerID)
	if err != nil {
		return s.normalize(ctx, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return s.normalize(ctx, err)
	}
	if n != 1 {
		return fault(NotFound, "owned root not found")
	}
	return nil
}

// RootRevoked verifies the owned root exists and reads its durable denial fact.
// Unknown, non-root and closed-store reads are errors, never permission grants.
func (s *Store) RootRevoked(ctx context.Context, scope Scope) (bool, error) {
	if err := s.check(ctx); err != nil {
		return false, err
	}
	if !validScope(scope) {
		return false, fault(Invalid, "caller and root required")
	}
	var revoked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM root_revocations r WHERE r.root_id=t.id AND r.caller=t.caller)
 FROM tasks t WHERE t.id=? AND t.root_id=? AND t.caller=? AND t.parent_id IS NULL`, scope.RootID, scope.RootID, scope.CallerID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fault(NotFound, "owned root not found")
	}
	if err != nil {
		return false, s.normalize(ctx, err)
	}
	return revoked, nil
}
