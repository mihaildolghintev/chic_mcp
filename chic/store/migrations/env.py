"""Alembic environment.

Two entry paths:
  - App startup passes the store's live (async-backed, sync-facing) connection
    via ``config.attributes["connection"]`` — see chic/store/db.py.
  - The CLI (``alembic upgrade``/``revision``) has no such connection and builds
    a sync engine from ``sqlalchemy.url`` in alembic.ini.

``render_as_batch=True`` so SQLite ALTER TABLE works via batch mode.
"""

from __future__ import annotations

from logging.config import fileConfig

from alembic import context
from sqlalchemy import engine_from_config, pool

from chic.store.models import Base

config = context.config
if config.config_file_name is not None:
    fileConfig(config.config_file_name)

target_metadata = Base.metadata


def _run(connection) -> None:
    context.configure(
        connection=connection, target_metadata=target_metadata, render_as_batch=True
    )
    with context.begin_transaction():
        context.run_migrations()


def run_offline() -> None:
    context.configure(
        url=config.get_main_option("sqlalchemy.url"),
        target_metadata=target_metadata,
        literal_binds=True,
        render_as_batch=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_online() -> None:
    connection = config.attributes.get("connection")
    if connection is not None:
        _run(connection)
        return
    engine = engine_from_config(
        config.get_section(config.config_ini_section, {}),
        prefix="sqlalchemy.",
        poolclass=pool.NullPool,
    )
    with engine.connect() as conn:
        _run(conn)


if context.is_offline_mode():
    run_offline()
else:
    run_online()
