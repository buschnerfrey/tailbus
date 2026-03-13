from __future__ import annotations


def _normalize_status(raw_status: str | None) -> str:
    """Return a normalized status string that is safe for comparison."""

    return (raw_status or "").casefold()


def filter_by_status(items: list[dict[str, str]], status: str) -> list[dict[str, str]]:
    """Return a filtered copy where the status matches the given value case-insensitively."""

    normalized_status = _normalize_status(status)
    if normalized_status == "all":
        return list(items)
    return [
        item
        for item in items
        if _normalize_status(item.get("status")) == normalized_status
    ]
