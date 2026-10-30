package bunstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// ConversationViewMigrationName is the bun/migrate name for the conversation-view
// tables (file prefix).
const ConversationViewMigrationName = "20260814000000"

func registerConversationViewMigration() {
	continuityMigrations.MustRegister(conversationViewMigrationUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

func conversationViewMigrationUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = sqliteConversationViewDDL()
	case dialect.PG:
		stmts = postgresConversationViewDDL()
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("bunstore: conversation view migrate: %w", err)
		}
	}
	return nil
}

func sqliteConversationViewDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS a_leg_conversation_view_state (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			state_revision INTEGER NOT NULL,
			next_slot_ordinal INTEGER NOT NULL,
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_never_backend_messages (
			a_leg_id TEXT NOT NULL,
			identity_version TEXT NOT NULL,
			identity_digest TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(a_leg_id, identity_version, identity_digest),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_steering_overlays (
			a_leg_id TEXT NOT NULL,
			overlay_id TEXT NOT NULL,
			overlay_revision INTEGER NOT NULL,
			slot_ordinal INTEGER NOT NULL,
			active INTEGER NOT NULL,
			message_version TEXT NOT NULL,
			message_role TEXT NOT NULL,
			message_text TEXT NOT NULL,
			placement_kind TEXT NOT NULL,
			anchor_identity_version TEXT NOT NULL DEFAULT '',
			anchor_identity_digest TEXT NOT NULL DEFAULT '',
			anchor_occurrence INTEGER NOT NULL DEFAULT 0,
			anchor_missing_policy TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			PRIMARY KEY(a_leg_id, overlay_id),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE,
			UNIQUE(a_leg_id, slot_ordinal)
		)`,
	}
}

func postgresConversationViewDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS a_leg_conversation_view_state (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			state_revision BIGINT NOT NULL,
			next_slot_ordinal BIGINT NOT NULL,
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_never_backend_messages (
			a_leg_id TEXT NOT NULL,
			identity_version TEXT NOT NULL,
			identity_digest TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix BIGINT NOT NULL,
			PRIMARY KEY(a_leg_id, identity_version, identity_digest),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_steering_overlays (
			a_leg_id TEXT NOT NULL,
			overlay_id TEXT NOT NULL,
			overlay_revision BIGINT NOT NULL,
			slot_ordinal BIGINT NOT NULL,
			active INTEGER NOT NULL,
			message_version TEXT NOT NULL,
			message_role TEXT NOT NULL,
			message_text TEXT NOT NULL,
			placement_kind TEXT NOT NULL,
			anchor_identity_version TEXT NOT NULL DEFAULT '',
			anchor_identity_digest TEXT NOT NULL DEFAULT '',
			anchor_occurrence BIGINT NOT NULL DEFAULT 0,
			anchor_missing_policy TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix BIGINT NOT NULL,
			updated_at_unix BIGINT NOT NULL,
			PRIMARY KEY(a_leg_id, overlay_id),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE,
			UNIQUE(a_leg_id, slot_ordinal)
		)`,
	}
}
