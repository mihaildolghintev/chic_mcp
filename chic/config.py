"""Application configuration.

Env var names and defaults MUST match the deploy contract in
``../chic-deploy/config/deploy.yml`` (which cannot be changed). Field names map
case-insensitively to env vars, so ``telegram_bot_token`` ← ``TELEGRAM_BOT_TOKEN``.

chic-deploy provides:
  env.clear:  PUBLIC_BASE_URL, LISTEN_ADDR=":8080", CACHE_DB=/data/cache.db,
              APP_DB=/data/app.db, LOG_FORMAT=json,
              PHOENIX_COLLECTOR_ENDPOINT=http://phoenix:6006, OPENAI_API_KEY=""
  env.secret: TELEGRAM_BOT_TOKEN, TELEGRAM_WEBHOOK_SECRET, ALLOWED_USER_IDS,
              MOYSKLAD_TOKEN, DEEPSEEK_API_KEY
"""

from __future__ import annotations

from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
    )

    # --- required secrets (chic-deploy env.secret) ---
    telegram_bot_token: str
    telegram_webhook_secret: str
    allowed_user_ids: str
    moysklad_token: str
    deepseek_api_key: str

    # --- clear config (chic-deploy env.clear) ---
    public_base_url: str
    listen_addr: str = ":8080"
    cache_db: str | None = None  # unset ⇒ response cache disabled
    app_db: str = "app.db"
    history_db: str | None = None  # unset ⇒ snapshot history (XYZ/forecast) disabled
    jobs_db: str | None = None  # unset ⇒ scheduler disabled; else APScheduler SQLite job store
    log_format: str = "text"  # deploy sets "json"
    log_level: str = "info"
    phoenix_collector_endpoint: str | None = None  # unset ⇒ tracing disabled
    openai_api_key: str = ""  # "" ⇒ vision disabled

    # --- optional tuning (defaults; overridable via env) ---
    # TODO(phase-5): verify model ids against DeepSeek/OpenAI docs before the
    # first real LLM call — prod relies on these defaults (deploy doesn't set them).
    deepseek_model: str = "deepseek-v4-flash"
    openai_model: str = "gpt-5.4-mini"
    deepseek_base_url: str = "https://api.deepseek.com/v1"
    openai_base_url: str = "https://api.openai.com/v1"
    summary_char_budget: int = 300_000  # runes; negative disables summarization
    app_env: str = "local"
    mcp_bearer_token: str | None = None  # unset ⇒ /mcp endpoint not mounted

    @property
    def allowed_ids(self) -> set[int]:
        """Parse the comma-separated Telegram id allowlist."""
        return {int(p) for p in self.allowed_user_ids.split(",") if p.strip()}

    @property
    def host_port(self) -> tuple[str, int]:
        """Split ``LISTEN_ADDR`` (e.g. ``:8080`` or ``0.0.0.0:8080``) for uvicorn."""
        raw = self.listen_addr.strip()
        host, _, port = raw.rpartition(":")
        # Binding all interfaces is intentional: the app runs inside a container
        # behind kamal-proxy.
        return (host or "0.0.0.0", int(port))  # nosec B104

    @property
    def has_vision(self) -> bool:
        return bool(self.openai_api_key.strip())

    @property
    def tracing_enabled(self) -> bool:
        return bool((self.phoenix_collector_endpoint or "").strip())


@lru_cache
def get_settings() -> Settings:
    return Settings()  # type: ignore[call-arg]  # values come from env/.env
