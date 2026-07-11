"""Download a message's photo as a base64 data URI for the vision model."""

from __future__ import annotations

import base64
from io import BytesIO

from aiogram import Bot
from aiogram.types import Message

MAX_PHOTO_BYTES = 20 << 20  # the Bot API refuses files over 20 MB


class PhotoTooLargeError(Exception):
    pass


async def photo_data_uri(bot: Bot, message: Message) -> str:
    if not message.photo:
        raise ValueError("message has no photo")
    largest = max(message.photo, key=lambda p: p.width * p.height)
    if largest.file_size and largest.file_size > MAX_PHOTO_BYTES:
        raise PhotoTooLargeError

    file = await bot.get_file(largest.file_id)
    if file.file_size and file.file_size > MAX_PHOTO_BYTES:
        raise PhotoTooLargeError
    if not file.file_path:
        raise ValueError("no file path for photo")

    buf = BytesIO()
    await bot.download_file(file.file_path, destination=buf)
    data = buf.getvalue()
    if len(data) > MAX_PHOTO_BYTES:
        raise PhotoTooLargeError

    # Telegram always re-encodes photos as JPEG.
    return "data:image/jpeg;base64," + base64.b64encode(data).decode()
