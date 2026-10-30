package bunstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

const ConversationBootstrapMigrationName = "20261008000000"

func registerConversationBootstrapMigration() {
	continuityMigrations.MustRegister(conversationBootstrapUp, func(context.Context, *bun.DB) error { return nil })
}

func conversationBootstrapUp(ctx context.Context, db *bun.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS a_leg_steering_bootstrap (
 a_leg_id TEXT NOT NULL,
 producer_id TEXT NOT NULL,
 outcome TEXT NOT NULL,
 matched_count INTEGER NOT NULL,
 model_evidence TEXT NOT NULL,
 PRIMARY KEY(a_leg_id, producer_id),
 FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
)`); err != nil {
		return fmt.Errorf("bunstore: conversation bootstrap migrate: %w", err)
	}
	return nil
}
