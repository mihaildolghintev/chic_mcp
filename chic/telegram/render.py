"""Rendering the agent's Markdown for Telegram via telegramify-markdown.

The agent answers in Markdown; telegramify converts it to MarkdownV2 (handling
all escaping). Long answers are split on the SOURCE text before conversion so
each chunk's MarkdownV2 entities stay balanced. If Telegram still rejects a
chunk, the caller resends the raw source text as-is (no custom parser).
"""

from __future__ import annotations

import telegramify_markdown

MAX_MESSAGE_LEN = 4096
_SPLIT_TARGET = 3500  # headroom under 4096 for MarkdownV2 escaping expansion


def source_chunks(text: str) -> list[str]:
    """Split raw text into <=_SPLIT_TARGET pieces, preferring newline boundaries."""
    if len(text) <= _SPLIT_TARGET:
        return [text]
    chunks: list[str] = []
    rest = text
    while len(rest) > _SPLIT_TARGET:
        cut = _SPLIT_TARGET
        newline = rest.rfind("\n", _SPLIT_TARGET // 2, _SPLIT_TARGET)
        if newline != -1:
            cut = newline + 1
        chunks.append(rest[:cut].rstrip("\n"))
        rest = rest[cut:]
    rest = rest.rstrip("\n")
    if rest:
        chunks.append(rest)
    return chunks


def to_markdown_v2(source: str) -> str:
    """Convert one source chunk to Telegram MarkdownV2."""
    return str(telegramify_markdown.markdownify(source))
