# Navidrome LRCLIB plugin

> [!IMPORTANT]
>
> Not work on NavidromeUI (expected)

NOTES (bahasa indon):

Beberapa behavior yang berbeda dengan penerapan di aplikasi flo:

- fetch lyrics via /rest/getLyrics -> artist, title -> ini bukan synced lyrics
- fetch lyrics via /rest/getLyricsBySongId -> id -> synced dan unsynced. timestamp pakai item `start` dan in ms

Klo di flo:

- fetch lyrics via /api/get ini endpoint punyanya LRCLIB
- timestamp bukan dalam ms (tapi parsed ke ms, cek LRCParser.swift)
- cek apakah compatible dengan logic untuk lyrics yang unstructured?

Pertimbangan:

- klo source nya "system", jangan pakai LRCLIBService?
- cek apakah compatible dengan LyricsView khususnya PlayerViewModel
- ga bisa dipakai bila offline. save lyrics ke local juga?
