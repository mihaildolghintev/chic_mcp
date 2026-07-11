"""initial schema (messages, user_memory, session_summary)

Baseline reproducing the Go goose migrations 0001-0003 as a single starting
point for the Python app.

Revision ID: 0001
Revises:
Create Date: 2026-07-12
"""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0001"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "messages",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("user_id", sa.BigInteger(), nullable=False),
        sa.Column("role", sa.String(), nullable=False),
        sa.Column("content", sa.Text(), nullable=False),
        sa.Column("created_at", sa.BigInteger(), nullable=False),
    )
    op.create_index("idx_messages_user", "messages", ["user_id", "id"])

    op.create_table(
        "user_memory",
        sa.Column("user_id", sa.BigInteger(), primary_key=True),
        sa.Column("key", sa.String(), primary_key=True),
        sa.Column("value", sa.Text(), nullable=False),
        sa.Column("updated_at", sa.BigInteger(), nullable=False),
    )

    op.create_table(
        "session_summary",
        sa.Column("user_id", sa.BigInteger(), primary_key=True),
        sa.Column("epoch", sa.BigInteger(), primary_key=True),
        sa.Column("summary", sa.Text(), nullable=False),
        sa.Column("up_to_id", sa.BigInteger(), nullable=False),
        sa.Column("updated_at", sa.BigInteger(), nullable=False),
    )


def downgrade() -> None:
    op.drop_table("session_summary")
    op.drop_table("user_memory")
    op.drop_index("idx_messages_user", table_name="messages")
    op.drop_table("messages")
