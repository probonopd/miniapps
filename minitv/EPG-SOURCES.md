# Programme guide sources

Checked 2026-10-26 against the 43 channels of `playlist.m3u`. None of these needs an account or key unless noted.
Times in all of them are local or UTC with explicit offsets; convert to epoch seconds.

## ARD programm-api (first-party JSON)

The guide that the ARD Mediathek live page itself loads. Covers the ARD family only.

```sh
curl -H 'Accept-Encoding: gzip' --compressed 'https://programm-api.ard.de/program/api/program?day=2026-10-26'
```

- Without `day` it returns today. `day=YYYY-MM-DD` works from yesterday to 7 days ahead (the 8th day: 404).
  `channelIds=swr` narrows to one channel. About 0.3 MB gzipped (today) to 1.4 MB (other days) for all channels.
- `channels[]` has `id` = daserste, one, alpha, tagesschau24, phoenix, arte, 3sat, kika, br, hr, mdr, ndr, rbb, sr,
  swr, wdr, radiobremen. One entry per broadcaster: no split into BR Nord/Sued, MDR states, NDR states, SWR RP
  (the SWR entry is the Baden-Wuerttemberg program, WDR is Cologne).
- Programmes: `channels[].timeSlots[][]`, each with `title`, `subline`, `synopsis`, `duration` (seconds),
  `beginNet` (ISO with offset, program start), `broadcastedOn`, `broadcastEnd` (null in several entries: use the
  start of the next one or `beginNet + duration`).
- Always fresh (`cache-control: max-age=0`), no ETag, CORS open. Undocumented, so it can change without notice.

### ARD now/next (small, more regionals)

`https://programm-api.ard.de/nownext/api/now` (14 KB): the current and next programme of 18 channels at once, with
`events[].title.short`, `currentStartDate`, `endDate`, `runningState`. Includes variants the day guide lacks: BRSUED,
MDRS, NDRNDS, RBB Brandenburg, SWR BW, WDR Koeln, Radio Bremen, DW. `.../nownext/api/channel?channel=<crid>` returns
about 12 events for one channel (crid = the key in `/now`, e.g. BRSUED). The `program` endpoint with such a crid
returns nothing. Source: Zapp's `documentation/ard-api.md`.

## ARTE guide (JSON, ARTE only)

```sh
curl --compressed 'https://api.arte.tv/api/emac/v4/de/web/pages/TV_GUIDE/?day=2026-10-26'
```

- `zones[0].content.data[]` is the day's list (27 entries). Start is `availability.upcomingDate` (UTC), length is
  `duration` (seconds); title `title`, `subtitle`, text `shortDescription`. Consecutive entries line up (start +
  duration is the next start).
- Other days work (+5 days tested). The language is in the path: `/de/` for the German feed, `/fr/` for French.
- The www.arte.tv `/api/rproxy/...` path is 404; use api.arte.tv.

## ORF (HTML only)

`https://tv.orf.at/program/orf1/index.html` (also `orf2`, `orf3`, `orfs` for ORF Sport+). One broadcast day, 06:00
to about 05:30 next day, 37 entries on ORF 1. Each is `<li class="broadcast" data-channel="orf1"
data-start-time="2026-10-26T06:00:08.000+01:00" data-end-time="...">` with `series-title`, `episode-title`. A `date`
parameter is ignored (always today). Scraping, so brittle; the XMLTV sources have ORF with 2 days.

## ZDF family (token from the live page; found in Zapp)

The ZDF API needs a bearer token, but the token is public: it sits in the HTML of the live page. Method of the
[zapp-backend](https://github.com/mediathekview/zapp-backend) (`downloader/zdf-downloader.js`), tested here:

```sh
# 1. token: regex  videoToken[\\s:"{]*apiToken[\\s:"]*([\w.-]{40,})  on the page
curl -s --compressed https://www.zdf.de/live-tv
# 2. programmes
curl -s --compressed -H 'Api-Auth: Bearer <token>' -H 'Accept: application/vnd.de.zdf.v1.0+json' \
  -H 'Origin: https://www.zdf.de' \
  'https://api.zdf.de/cmdm/epg/broadcasts?limit=200&page=1&order=asc&tvServices=zdf&from=2026-10-26T13:00:00Z&to=2026-10-29T13:00:00Z'
```

- `tvServices`: `zdf`, `3sat`, `ki.ka`, `phoenix`, `zdfinfo`, `ZDFneo`. 3 days of ZDF = 86 broadcasts, 107 KB.
- Result: `http://zdf.de/rels/cmdm/broadcasts[]` with `title`, `subtitle`, `text`, `airtimeBegin`, `airtimeEnd`
  (ISO with offset), `duration`. Without a token: 403.
- Brittle in one way: the token regex depends on the page markup. If the page changes, the source fails and the
  next one (XMLTV) takes over.

## Others

- phoenix, KiKA, ONE, ARD alpha, tagesschau24, ARTE, 3sat: in the ARD programm-api (and 3sat, KiKA, phoenix also
  in the ZDF API above).
- DW: Zapp documents `https://api.dw.com/api/epg/4?languageId=1&days=1`, which is 404 today. The ARD `/now` has DW
  now/next. The DW web app uses `https://www.dw.com/graphql` (seen in MediaHubMX/addons `dw/addon.mjs`; not tested).
- ORF (days 2+), regional splits (MDR Sachsen/Thueringen/Sachsen-Anhalt, NDR states, SWR RP, BR Nord/Sued,
  RBB Berlin/Brandenburg): only in the XMLTV sources (FreeEPG, Kaimandura). SRF: no open JSON API found (only a guessed
  SRG integration layer URL was tried, 404; not investigated further).
- WDR Lokalzeit channels: in no source.

## XMLTV (all channels, large)

| Source | URL | Download | Update |
|---|---|---|---|
| FreeEPG | `https://www.free-epg.de/api/epg/de.xml.gz` | 7.8 MB (HTTP gzip), 52 MB XML, ETag | hourly, about 5.5 days |
| Kaimandura | `https://kaimandura.github.io/epg-de/DE-MASTER.xml.gz` | 13.2 MB, 98 MB XML, ETag, Range | daily 05:44 UTC, about 6.5 days |

FreeEPG's `.gz` is plain XML on the wire (gzip is HTTP encoding); detect by magic bytes. Coverage of the 43
channels: FreeEPG 32, Kaimandura 29, together 33. EPG Full (paid, per IPTV provider, personal link) and a guide
made with iptv-org/epg are possible sources for a later, fuller guide; not tested.

## How Zapp does it (and what minitv uses)

minitv asks the public Zapp backend for the current programme (`https://api.zapp.mediathekview.de/v1/shows/<id>`, 30 channels).

[Zapp](https://github.com/mediathekview/zapp) (Android, live TV of the public broadcasters) has no EPG data of its
own. It asks its server, [zapp-backend](https://github.com/mediathekview/zapp-backend) (node.js, one JSON per
channel at `/v1/shows/<channel>`, cached), which fetches only the current programme from: the ARD programm-api
`nownext` for ARD and regionals, the ZDF API with the scraped token for ZDF, 3sat, KiKA, phoenix, ZDFinfo and
ZDFneo, the DW API, and the parliament channels. Only now (and next), no schedule, no XMLTV.

## Suggested order

1. ARD programm-api (`program` for days, `nownext` for regionals): ARD family (small, first-party).
2. ZDF API with the scraped token: ZDF, ZDF neo, ZDF info, 3sat.
3. ARTE guide: ARTE DE (and ARTE FR via `/fr/`).
4. FreeEPG, then Kaimandura: everything else and fallback for all of the above.
